package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Token is a static CI token, without the token itself: only its hash is
// stored.
type Token struct {
	ID         int64
	Repo       string // the repo's CI subject
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

// CreateToken stores the hash of a new token for the repo with the given CI
// subject, creating the repo, and audits it.
func (s *Store) CreateToken(ctx context.Context, repo string, hash []byte, expires time.Time, actor string) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			WITH r AS (
				INSERT INTO repos (ci_subject) VALUES ($1)
				ON CONFLICT (ci_subject) DO UPDATE SET ci_subject = excluded.ci_subject
				RETURNING id)
			INSERT INTO ci_tokens (repo_id, hash, expires_at, created_by)
			SELECT r.id, $2, $3, $4 FROM r RETURNING id`,
			repo, hash, expires, actor).Scan(&id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "token.create", repo,
			map[string]any{"token": id, "expires": expires.UTC().Format(time.RFC3339)})
	})
	return id, err
}

// TokenByHash returns the live token with the given hash and marks it used,
// or nil if there is none, or it has expired or been revoked.
func (s *Store) TokenByHash(ctx context.Context, hash []byte) (*Token, error) {
	var t Token
	err := s.pool.QueryRow(ctx, `
		UPDATE ci_tokens t SET last_used_at = now()
		FROM repos r
		WHERE t.hash = $1 AND r.id = t.repo_id AND t.revoked_at IS NULL AND t.expires_at > now()
		RETURNING t.id, r.ci_subject, t.created_by, t.created_at, t.expires_at, t.revoked_at, t.last_used_at`,
		hash).Scan(&t.ID, &t.Repo, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Tokens lists every token, newest first, including expired and revoked
// ones.
func (s *Store) Tokens(ctx context.Context) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, r.ci_subject, t.created_by, t.created_at, t.expires_at, t.revoked_at, t.last_used_at
		FROM ci_tokens t JOIN repos r ON r.id = t.repo_id
		ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Token, error) {
		var t Token
		err := r.Scan(&t.ID, &t.Repo, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt)
		return t, err
	})
}

// ErrNoToken means there is no live token with that id.
var ErrNoToken = errors.New("no such token, or it is revoked already")

// RevokeToken revokes a token and audits it.
func (s *Store) RevokeToken(ctx context.Context, id int64, actor string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var repo string
		err := tx.QueryRow(ctx, `
			UPDATE ci_tokens t SET revoked_at = now() FROM repos r
			WHERE t.id = $1 AND t.revoked_at IS NULL AND r.id = t.repo_id
			RETURNING r.ci_subject`, id).Scan(&repo)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoToken
		}
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, "token.revoke", repo, map[string]any{"token": id})
	})
}
