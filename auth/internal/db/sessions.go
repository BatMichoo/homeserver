package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/model"
)

// CreateSession stores a new session under the hash of its token.
func (d *DB) CreateSession(ctx context.Context, tokenHash []byte, userID, userAgent string, now, expires time.Time) error {
	_, err := d.SQL.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, user_agent, created_at, last_seen_at, expires_at)
		 VALUES ($1, $2, $3, $4, $4, $5)`, tokenHash, userID, userAgent, now, expires)
	return err
}

// SessionUser finds the user of a session that hasn't expired at now,
// with the session's expiry and last extension. model.ErrNoSession if
// there is none.
func (d *DB) SessionUser(ctx context.Context, tokenHash []byte, now time.Time) (model.User, time.Time, time.Time, error) {
	var u model.User
	var expires, lastSeen time.Time
	err := d.SQL.QueryRowContext(ctx,
		`SELECT u.id, u.username, COALESCE(u.display_name, ''), COALESCE(u.email, ''), u.created_at,
		        s.expires_at, s.last_seen_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1 AND s.expires_at > $2`, tokenHash, now).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.CreatedAt, &expires, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, time.Time{}, time.Time{}, model.ErrNoSession
	}
	return u, expires, lastSeen, err
}

// TouchSession moves a session's expiry and last extension.
func (d *DB) TouchSession(ctx context.Context, tokenHash []byte, now, expires time.Time) error {
	_, err := d.SQL.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE token_hash = $1`, tokenHash, now, expires)
	return err
}

// DeleteSession ends one session.
func (d *DB) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := d.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteUserSessions ends every session of a user except the one whose
// hash is except (nil: all of them).
func (d *DB) DeleteUserSessions(ctx context.Context, userID string, except []byte) error {
	_, err := d.SQL.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND ($2::bytea IS NULL OR token_hash <> $2)`, userID, except)
	return err
}

// DeleteExpiredSessions removes sessions that expired before now.
func (d *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.SQL.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
