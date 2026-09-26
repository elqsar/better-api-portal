package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is a signed-in user of the web UI, as their ID token described
// them at sign-in.
type Session struct {
	Subject   string
	Name      string
	Email     string
	Groups    []string
	ExpiresAt time.Time
}

// CreateSession stores a session under the hash of its cookie value, and
// removes expired ones.
func (s *Store) CreateSession(ctx context.Context, idHash []byte, sess Session) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
			return err
		}
		groups := sess.Groups
		if groups == nil {
			groups = []string{}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions (id_hash, subject, name, email, groups, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			idHash, sess.Subject, sess.Name, sess.Email, groups, sess.ExpiresAt)
		return err
	})
}

// Session returns the live session with the hash, or nil. Its last use is
// recorded at most once a minute.
func (s *Store) Session(ctx context.Context, idHash []byte) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		WITH touched AS (
			UPDATE sessions SET last_seen_at = now()
			WHERE id_hash = $1 AND expires_at > now() AND last_seen_at < now() - interval '1 minute')
		SELECT subject, name, email, groups, expires_at FROM sessions
		WHERE id_hash = $1 AND expires_at > now()`, idHash).
		Scan(&sess.Subject, &sess.Name, &sess.Email, &sess.Groups, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, idHash []byte) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash)
	return err
}

// Published is a published version, for lists.
type Published struct {
	APIID, Kind, Owner, Lifecycle, Semver string
	PublishedAt                           time.Time
}

// RecentlyPublished lists the latest published versions across APIs, newest
// first, and how many APIs have a published version.
func (s *Store) RecentlyPublished(ctx context.Context, limit int) ([]Published, int, error) {
	var apis int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM apis WHERE latest_version_id IS NOT NULL`).Scan(&apis); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT v.api_id, a.kind, a.owner, a.lifecycle, v.semver, v.created_at
		FROM versions v JOIN apis a ON a.id = v.api_id
		WHERE v.status = 'published'
		ORDER BY v.created_at DESC, v.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, 0, err
	}
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Published, error) {
		var p Published
		err := r.Scan(&p.APIID, &p.Kind, &p.Owner, &p.Lifecycle, &p.Semver, &p.PublishedAt)
		return p, err
	})
	return ps, apis, err
}
