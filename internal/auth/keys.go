package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"gite/internal/db"
)

// SSHKey is one registered public key.
type SSHKey struct {
	ID          int64
	UserID      int64
	Username    string
	Title       string
	Fingerprint string
	KeyType     string
	Pubkey      string
	Comment     string
}

// ParsePublicKey validates raw authorized_keys style key data.
func ParsePublicKey(data []byte) (ssh.PublicKey, string, error) {
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(bytes.TrimSpace(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: invalid public key: %v", ErrInvalid, err)
	}
	return pub, comment, nil
}

// AddKey validates and stores a public key for a user.
// A key can only ever belong to one account, enforced by the unique fingerprint.
func AddKey(ctx context.Context, d *db.DB, u *User, title string, data []byte) (*SSHKey, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("%w: key title is required", ErrInvalid)
	}
	pub, comment, err := ParsePublicKey(data)
	if err != nil {
		return nil, err
	}
	fingerprint := ssh.FingerprintSHA256(pub)
	keyType := pub.Type()
	pubkey := keyType + " " + base64.StdEncoding.EncodeToString(pub.Marshal())
	res, err := d.ExecContext(ctx,
		`INSERT INTO ssh_keys (user_id, title, fingerprint, key_type, pubkey, comment)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, title, fingerprint, keyType, pubkey, comment)
	if err != nil {
		if db.IsUnique(err) {
			return nil, fmt.Errorf("%w: key %s already registered", ErrConflict, fingerprint)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &SSHKey{
		ID: id, UserID: u.ID, Username: u.Username, Title: title,
		Fingerprint: fingerprint, KeyType: keyType, Pubkey: pubkey, Comment: comment,
	}, nil
}

// UserKeyByFingerprint resolves a connecting key to its account.
// This is the only lookup the ssh transport needs for authentication.
func UserKeyByFingerprint(ctx context.Context, d *db.DB, fingerprint string) (*SSHKey, *User, error) {
	var k SSHKey
	var u User
	err := d.QueryRowContext(ctx, `
		SELECT k.id, k.user_id, k.title, k.fingerprint, k.key_type, k.pubkey, k.comment,
		       u.id, u.username, u.email, u.password_hash, u.is_admin, u.created_at
		FROM ssh_keys k JOIN users u ON u.id = k.user_id
		WHERE k.fingerprint = ?`, fingerprint).
		Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.KeyType, &k.Pubkey, &k.Comment,
			&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: ssh key", ErrNotFound)
	}
	if err != nil {
		return nil, nil, err
	}
	k.Username = u.Username
	return &k, &u, nil
}

// ListKeys returns the keys of one account.
func ListKeys(ctx context.Context, d *db.DB, u *User) ([]*SSHKey, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, user_id, title, fingerprint, key_type, pubkey, comment FROM ssh_keys
		 WHERE user_id = ? ORDER BY id`, u.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.KeyType, &k.Pubkey, &k.Comment); err != nil {
			return nil, err
		}
		k.Username = u.Username
		out = append(out, &k)
	}
	return out, rows.Err()
}

// RemoveKey deletes a key by fingerprint. Returns ErrNotFound when the key
// is not registered.
func RemoveKey(ctx context.Context, d *db.DB, fingerprint string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM ssh_keys WHERE fingerprint = ?`, fingerprint)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: ssh key", ErrNotFound)
	}
	return nil
}
