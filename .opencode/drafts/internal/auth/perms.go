package auth

import (
	"context"
	"fmt"

	"gite/internal/db"
)

// Role is a github-style permission level on one repository.
type Role int

const (
	RoleNone Role = iota
	RoleRead
	RoleWrite
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RoleRead:
		return "read"
	case RoleWrite:
		return "write"
	case RoleAdmin:
		return "admin"
	default:
		return "none"
	}
}

// ParseRole turns a role name into a Role.
func ParseRole(s string) (Role, error) {
	switch s {
	case "read":
		return RoleRead, nil
	case "write":
		return RoleWrite, nil
	case "admin":
		return RoleAdmin, nil
	default:
		return RoleNone, fmt.Errorf("%w: role must be read, write or admin", ErrInvalid)
	}
}

// RepoInfo is the slice of repository state the permission model needs.
// internal/repository.Repo satisfies it.
type RepoInfo interface {
	RepoID() int64
	OwnerUserID() int64
	IsPrivate() bool
}

func rankOf(role string) Role {
	switch role {
	case "admin":
		return RoleAdmin
	case "write":
		return RoleWrite
	case "read":
		return RoleRead
	default:
		return RoleNone
	}
}

// Evaluate returns the effective access level of u on repo.
// Owner and site admins are implicitly admin. Public repos grant read
// to every authenticated user and to anonymous as well.
func Evaluate(ctx context.Context, d *db.DB, u *User, ri RepoInfo) Role {
	if u == nil {
		if ri.IsPrivate() {
			return RoleNone
		}
		return RoleRead
	}
	if u.IsAdmin || u.ID == ri.OwnerUserID() {
		return RoleAdmin
	}
	var role string
	err := d.QueryRowContext(ctx,
		`SELECT role FROM repo_collabs WHERE repo_id = ? AND user_id = ?`,
		ri.RepoID(), u.ID).Scan(&role)
	got := rankOf(role) // sql.ErrNoRows leaves role empty -> RoleNone
	if !ri.IsPrivate() && got < RoleRead {
		got = RoleRead
	}
	_ = err
	return got
}

// Require enforces the wanted role and explains shortfalls.
//
// Rule: a private repo the caller cannot even read is reported as not
// found, so unauthorized users learn nothing about its existence. Public
// repos reveal themselves, so a missing write role there is an explicit
// access-denied message.
func Require(ctx context.Context, d *db.DB, u *User, ri RepoInfo, want Role) error {
	got := Evaluate(ctx, d, u, ri)
	if got >= want {
		return nil
	}
	if ri.IsPrivate() && want <= RoleRead && got < RoleRead {
		return fmt.Errorf("%w: repository not found", ErrNotFound)
	}
	return fmt.Errorf("%w: %s access required", ErrAccessDenied, want)
}

// Collab is one collaborator row with its username.
type Collab struct {
	UserID   int64
	Username string
	Role     string
}

// Grant sets a collaborator role, upserting an existing entry.
func Grant(ctx context.Context, d *db.DB, repoID, userID int64, role string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO repo_collabs (repo_id, user_id, role) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, user_id) DO UPDATE SET role = excluded.role`,
		repoID, userID, role)
	if err != nil {
		if db.IsUnique(err) {
			return fmt.Errorf("%w: collaborator", ErrConflict)
		}
		return err
	}
	return nil
}

// Revoke removes a collaborator. ErrNotFound when there is none.
func Revoke(ctx context.Context, d *db.DB, repoID, userID int64) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM repo_collabs WHERE repo_id = ? AND user_id = ?`, repoID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: collaborator", ErrNotFound)
	}
	return nil
}

// ListCollabs returns every explicit collaborator of a repo.
func ListCollabs(ctx context.Context, d *db.DB, repoID int64) ([]*Collab, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT c.user_id, u.username, c.role
		FROM repo_collabs c JOIN users u ON u.id = c.user_id
		WHERE c.repo_id = ? ORDER BY u.username`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Collab
	for rows.Next() {
		var c Collab
		if err := rows.Scan(&c.UserID, &c.Username, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}
