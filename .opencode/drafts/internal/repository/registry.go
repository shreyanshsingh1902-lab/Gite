// Package repository manages the catalog of repositories and their bare
// directories on disk.
//
// A gite repo is a real, ordinary bare git repo created with the native
// git binary. Any git tool can read it directly; gite adds the catalog,
// access control and (later) alternate backends on top.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gite/internal/auth"
	"gite/internal/db"
	"gite/internal/git"
)

// Repo is one catalog row joined with its owner's username.
type Repo struct {
	ID            int64
	OwnerID       int64
	OwnerUsername string
	Name          string
	Description   string
	Private       bool
	Backend       string // server | agent | external | mirror
	DefaultBranch string
	CreatedAt     string
}

func (r *Repo) RepoID() int64      { return r.ID }
func (r *Repo) OwnerUserID() int64 { return r.OwnerID }
func (r *Repo) IsPrivate() bool    { return r.Private }

// FullName renders owner/name.
func (r *Repo) FullName() string { return r.OwnerUsername + "/" + r.Name }

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
var branchRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)

func validateName(raw string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(raw))
	if len(n) < 1 || len(n) > 64 {
		return "", fmt.Errorf("%w: repository name must be 1-64 characters", auth.ErrInvalid)
	}
	if !nameRe.MatchString(n) {
		return "", fmt.Errorf("%w: repository name may contain a-z, 0-9, '.', '_' and '-'", auth.ErrInvalid)
	}
	if strings.HasSuffix(n, ".git") {
		return "", fmt.Errorf("%w: repository name must not end with .git", auth.ErrInvalid)
	}
	return n, nil
}

func validateBranch(raw string) (string, error) {
	b := strings.ToLower(strings.TrimSpace(raw))
	if b == "" {
		return "main", nil
	}
	if !branchRe.MatchString(b) {
		return "", fmt.Errorf("%w: branch name may contain a-z, 0-9, '.', '_', '/' and '-'", auth.ErrInvalid)
	}
	if strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.Contains(b, "..") {
		return "", fmt.Errorf("%w: branch name is not valid", auth.ErrInvalid)
	}
	return b, nil
}

// Store is the registry plus disk layout.
type Store struct {
	DB       *db.DB
	RepoRoot string
}

// NewStore builds a store over one repository root.
func NewStore(d *db.DB, repoRoot string) *Store {
	return &Store{DB: d, RepoRoot: repoRoot}
}

// Path is the on-disk location of a bare repository.
func (s *Store) Path(r *Repo) string {
	return filepath.Join(s.RepoRoot, r.OwnerUsername, r.Name+".git")
}

// Create registers a repository row, then initializes its bare git dir.
// The row goes in first so a unique violation blocks two racing creates.
func (s *Store) Create(ctx context.Context, owner *auth.User, name, description string, private bool, branch string) (*Repo, error) {
	name, err := validateName(name)
	if err != nil {
		return nil, err
	}
	branch, err = validateBranch(branch)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.RepoRoot, owner.Username, name+".git")
	if _, statErr := os.Stat(dir); statErr == nil {
		return nil, fmt.Errorf("repository directory already exists on disk: %s", dir)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", dir, statErr)
	}

	privInt := 1
	if !private {
		privInt = 0
	}
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO repos (owner_id, name, description, private, backend, default_branch)
		 VALUES (?, ?, ?, ?, 'server', ?)`,
		owner.ID, name, description, privInt, branch)
	if err != nil {
		if db.IsUnique(err) {
			return nil, fmt.Errorf("%w: repository %s/%s already exists", auth.ErrConflict, owner.Username, name)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()

	rollback := func(cause error) (*Repo, error) {
		_, delErr := s.DB.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
		if delErr != nil {
			return nil, fmt.Errorf("%w (cleanup delete row failed: %v)", cause, delErr)
		}
		return nil, cause
	}

	if _, err := git.Run(ctx, "init", "--bare", "--quiet", dir); err != nil {
		return rollback(fmt.Errorf("init bare repository: %w", err))
	}
	if _, err := git.RunIn(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return rollback(fmt.Errorf("set default branch: %w", err))
	}
	return s.getByID(ctx, id)
}

const repoCols = `
	SELECT r.id, r.owner_id, u.username, r.name, r.description, r.private,
	       r.backend, r.default_branch, r.created_at
	FROM repos r JOIN users u ON u.id = r.owner_id`

func scanRepo(row interface{ Scan(...any) error }) (*Repo, error) {
	var (
		r        Repo
		privInt  int
		descr    string
	)
	if err := row.Scan(&r.ID, &r.OwnerID, &r.OwnerUsername, &r.Name, &descr,
		&privInt, &r.Backend, &r.DefaultBranch, &r.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: repository", auth.ErrNotFound)
		}
		return nil, err
	}
	r.Description = descr
	r.Private = privInt != 0
	return &r, nil
}

func (s *Store) getByID(ctx context.Context, id int64) (*Repo, error) {
	return scanRepo(s.DB.QueryRowContext(ctx, repoCols+` WHERE r.id = ?`, id))
}

// GetByOwnerName resolves owner/name (case-insensitive) to a repo.
func (s *Store) GetByOwnerName(ctx context.Context, ownerUsername, name string) (*Repo, error) {
	return scanRepo(s.DB.QueryRowContext(ctx,
		repoCols+` WHERE u.username = ? COLLATE NOCASE AND r.name = ? COLLATE NOCASE`,
		strings.ToLower(ownerUsername), strings.ToLower(name)))
}

// List returns repos, optionally filtered by owner. Empty filter = all.
func (s *Store) List(ctx context.Context, ownerUsername string) ([]*Repo, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if ownerUsername != "" {
		rows, err = s.DB.QueryContext(ctx, repoCols+
			` WHERE u.username = ? COLLATE NOCASE ORDER BY u.username, r.name`, strings.ToLower(ownerUsername))
	} else {
		rows, err = s.DB.QueryContext(ctx, repoCols+` ORDER BY u.username, r.name`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes the catalog row (collaborators cascade) and the bare dir.
func (s *Store) Delete(ctx context.Context, r *Repo) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, r.ID); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Path(r)); err != nil {
		return fmt.Errorf("remove %s: %w", s.Path(r), err)
	}
	return nil
}
