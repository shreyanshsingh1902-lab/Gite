// Package db owns the sqlite connection and the schema migrations.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go sqlite, keeps the single-binary property
)

// ErrConflict marks a unique-constraint violation, re-thrown for callers.
var ErrConflict = errors.New("already exists")

// IsUnique reports whether err is a unique-constraint violation.
func IsUnique(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// DB wraps the sqlite handle.
type DB struct {
	*sql.DB
}

// Open opens (and creates) the sqlite database with durable pragmas.
func Open(path string) (*DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// One handle for phase 0; the write path is serialized anyway.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &DB{DB: d}, nil
}

// migrations runs in order; each entry upgrades the schema by one version.
var migrations = []string{
	// v1: forge floor. Includes phase 1-3 tables now so later migrations
	// start from one known baseline.
	`
CREATE TABLE users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	email TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE ssh_keys (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title TEXT NOT NULL,
	fingerprint TEXT NOT NULL UNIQUE,
	key_type TEXT NOT NULL,
	pubkey TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	added_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE pat_tokens (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	scopes TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	expires_at TEXT,
	last_used_at TEXT
);
CREATE TABLE sessions (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	cookie_hash TEXT NOT NULL UNIQUE,
	expires_at TEXT NOT NULL
);
CREATE TABLE repos (
	id INTEGER PRIMARY KEY,
	owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT '',
	private INTEGER NOT NULL DEFAULT 1,
	backend TEXT NOT NULL DEFAULT 'server',
	default_branch TEXT NOT NULL DEFAULT 'main',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	UNIQUE (owner_id, name)
);
CREATE TABLE repo_collabs (
	repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role TEXT NOT NULL CHECK (role IN ('read','write','admin')),
	PRIMARY KEY (repo_id, user_id)
);
CREATE TABLE agents (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'offline',
	last_seen_at TEXT,
	UNIQUE (user_id, name)
);
CREATE TABLE agent_repos (
	agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
	repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	path TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (agent_id, repo_id)
);
CREATE TABLE repo_remotes (
	id INTEGER PRIMARY KEY,
	repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
	url TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'generic',
	is_primary INTEGER NOT NULL DEFAULT 0,
	UNIQUE (repo_id, url)
);
CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys (fingerprint);
CREATE INDEX idx_repos_owner ON repos (owner_id);
`,
}

// Migrate applies pending migrations inside a transaction.
func (d *DB) Migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i, m := range migrations {
		v := i + 1
		if v <= current {
			continue
		}
		tx, err := d.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", v, err)
		}
		if _, err := tx.Exec(m); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w\n%s", v, err, m)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", v, err)
		}
	}
	return nil
}
