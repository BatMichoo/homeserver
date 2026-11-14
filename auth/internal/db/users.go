package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/BatMichoo/homeserver/auth/internal/model"
	"github.com/lib/pq"
)

// DB returns the connection pool (tests read rows through it).
func (d *DB) DB() *sql.DB { return d.SQL }

const userColumns = `id, username, COALESCE(display_name, ''), COALESCE(email, ''), created_at, password_hash`

func scanUser(row interface{ Scan(...any) error }) (model.User, string, error) {
	var u model.User
	var hash string
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.CreatedAt, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, "", model.ErrUnknownUser
	}
	return u, hash, err
}

// CreateUser adds a user. A taken username is model.ErrUsernameTaken.
func (d *DB) CreateUser(ctx context.Context, username, passwordHash string) (model.User, error) {
	u, _, err := scanUser(d.SQL.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING `+userColumns,
		username, passwordHash))
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		return model.User{}, model.ErrUsernameTaken
	}
	return u, err
}

// UserByUsername finds a user and their password hash (exact match).
func (d *DB) UserByUsername(ctx context.Context, username string) (model.User, string, error) {
	return scanUser(d.SQL.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username))
}

// UserByID finds a user and their password hash.
func (d *DB) UserByID(ctx context.Context, id string) (model.User, string, error) {
	return scanUser(d.SQL.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// UpdateProfile sets the display name and email ("" clears them).
func (d *DB) UpdateProfile(ctx context.Context, id, displayName, email string) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE users SET display_name = NULLIF($2, ''), email = NULLIF($3, ''), updated_at = now() WHERE id = $1`,
		id, displayName, email)
	return err
}

// SetPasswordHash replaces a user's password hash.
func (d *DB) SetPasswordHash(ctx context.Context, id, hash string) error {
	_, err := d.SQL.ExecContext(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	return err
}
