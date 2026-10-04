package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserToken is a personal access token, without the token itself: only
// its hash is stored.
type UserToken struct {
	ID         int64
	Label      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
}

// CreateUserToken stores the hash of a new token for the session's user,
// and audits it.
func (s *Store) CreateUserToken(ctx context.Context, hash []byte, sess Session, label string, expires time.Time) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO user_tokens (hash, subject, name, email, groups, label, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			hash, sess.Subject, sess.Name, sess.Email, nonNil(sess.Groups), label, expires).Scan(&id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor(sess), "user-token.create", sess.Subject,
			map[string]any{"token": id, "label": label, "expires": expires.UTC().Format(time.RFC3339)})
	})
	return id, err
}

// UserTokenSession returns the user a live token stands for, as a session
// that ends when the token expires, or nil. Its last use is recorded at
// most once a minute.
func (s *Store) UserTokenSession(ctx context.Context, hash []byte) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		WITH touched AS (
			UPDATE user_tokens SET last_used_at = now()
			WHERE hash = $1 AND revoked_at IS NULL AND expires_at > now()
			  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute'))
		SELECT subject, name, email, groups, expires_at FROM user_tokens
		WHERE hash = $1 AND revoked_at IS NULL AND expires_at > now()`, hash).
		Scan(&sess.Subject, &sess.Name, &sess.Email, &sess.Groups, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// UserTokens lists the user's tokens that aren't revoked, newest first.
func (s *Store) UserTokens(ctx context.Context, subject string) ([]UserToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, created_at, expires_at, last_used_at FROM user_tokens
		WHERE subject = $1 AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, subject)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (UserToken, error) {
		var t UserToken
		err := r.Scan(&t.ID, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt)
		return t, err
	})
}

// RevokeUserToken revokes one of the user's tokens and audits it. It
// reports whether there was such a token to revoke.
func (s *Store) RevokeUserToken(ctx context.Context, id int64, sess Session) (bool, error) {
	var found bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE user_tokens SET revoked_at = now()
			WHERE id = $1 AND subject = $2 AND revoked_at IS NULL`, id, sess.Subject)
		if err != nil {
			return err
		}
		if found = tag.RowsAffected() == 1; !found {
			return nil
		}
		return audit(ctx, tx, actor(sess), "user-token.revoke", sess.Subject, map[string]any{"token": id})
	})
	return found, err
}

// actor names a user in the audit log.
func actor(sess Session) string {
	if sess.Email != "" {
		return sess.Email
	}
	return sess.Subject
}
