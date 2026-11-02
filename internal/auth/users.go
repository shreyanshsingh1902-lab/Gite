// Package auth holds accounts, ssh keys and the permission model.
//
// Every transport door (ssh, http, web viewer) funnels its access decision
// through Evaluate/Require here. One choke point, no exceptions.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"gite/internal/db"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("already exists")
	ErrInvalid      = errors.New("invalid input")
	ErrAccessDenied = errors.New("access denied")
)

// User is a gite account.
type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    string
}

var usernameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$`)

func validateUsername(raw string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(raw))
	if len(u) < 2 || len(u) > 32 {
		return "", fmt.Errorf("%w: username must be 2-32 characters", ErrInvalid)
	}
	if !usernameRe.MatchString(u) {
		return "", fmt.Errorf("%w: username may contain a-z, 0-9, '-' and '_' and must not start or end with '-' or '_'", ErrInvalid)
	}
	return u, nil
}

// CreateUser validates and stores a new account. Usernames are stored lower
// case so filesystem paths stay predictable.
func CreateUser(ctx context.Context, d *db.DB, username, email, password string, admin bool) (*User, error) {
	u, err := validateUsername(username)
	if err != nil {
		return nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%w: email is not valid", ErrInvalid)
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalid)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	isAdmin := 0
	if admin {
		isAdmin = 1
	}
	res, err := d.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		u, email, string(hash), isAdmin)
	if err != nil {
		if db.IsUnique(err) {
			return nil, fmt.Errorf("%w: username or email already registered", ErrConflict)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: u, Email: email, IsAdmin: admin}, nil
}

const userCols = `id, username, email, password_hash, is_admin, created_at`

func scanUser(row *sql.Row) (*User, error) {
	var (
		u      User
		isAdm  int
		hash   string
	)
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &hash, &isAdm, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: user", ErrNotFound)
		}
		return nil, err
	}
	u.PasswordHash = hash
	u.IsAdmin = isAdm != 0
	return &u, nil
}

// GetUserByUsername looks a user up, case-insensitive.
func GetUserByUsername(ctx context.Context, d *db.DB, username string) (*User, error) {
	row := d.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username = ? COLLATE NOCASE`,
		strings.ToLower(strings.TrimSpace(username)))
	return scanUser(row)
}

// ListUsers returns every account, oldest first.
func ListUsers(ctx context.Context, d *db.DB) ([]*User, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var (
			u     User
			isAdm int
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &isAdm, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = isAdm != 0
		out = append(out, &u)
	}
	return out, rows.Err()
}
