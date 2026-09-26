// Package store is the Postgres repository behind `portal serve`
// (docs/spec/05-architecture.md §Storage). The pipeline packages must not
// import it; it depends on them only for the report types it persists.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"golang.org/x/mod/semver"

	"better-api-portal/internal/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Version statuses.
const (
	StatusPublished = "published"
	StatusRejected  = "rejected"
)

// ErrClaimed means the API id is claimed by another repo.
var ErrClaimed = errors.New("api is claimed by another repo")

// ErrVersionExists means a published version with that semver exists already.
var ErrVersionExists = errors.New("version is already published")

// Store is a connection pool with the portal's queries.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database at dsn. It doesn't migrate.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks the database is reachable, for /readyz.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies pending migrations. It holds a Postgres advisory lock, so
// replicas starting together migrate once.
func (s *Store) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()
	return migrate(ctx, db)
}

func migrate(ctx context.Context, db *sql.DB) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, mustSub(migrations, "migrations"),
		goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// Team is a team from the configuration.
type Team struct {
	Slug, Name, OIDCGroup string
}

// SyncTeams makes the teams table match the configuration. Teams that
// disappeared are kept while an API still names them as owner.
func (s *Store) SyncTeams(ctx context.Context, teams []Team) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		slugs := make([]string, 0, len(teams))
		for _, t := range teams {
			slugs = append(slugs, t.Slug)
			if _, err := tx.Exec(ctx, `
				INSERT INTO teams (slug, name, oidc_group) VALUES ($1, $2, $3)
				ON CONFLICT (slug) DO UPDATE SET name = excluded.name, oidc_group = excluded.oidc_group`,
				t.Slug, t.Name, t.OIDCGroup); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			DELETE FROM teams WHERE slug <> ALL($1)
			AND NOT EXISTS (SELECT 1 FROM apis WHERE apis.owner = teams.slug)`, slugs)
		return err
	})
}

// Repo returns the id of the repo with the given CI subject, creating it.
func (s *Store) Repo(ctx context.Context, ciSubject string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO repos (ci_subject) VALUES ($1)
		ON CONFLICT (ci_subject) DO UPDATE SET ci_subject = excluded.ci_subject
		RETURNING id`, ciSubject).Scan(&id)
	return id, err
}

// ClaimOwner returns the CI subject of the repo that claimed the API, or ""
// if it is unclaimed.
func (s *Store) ClaimOwner(ctx context.Context, apiID string) (string, error) {
	var subject string
	err := s.pool.QueryRow(ctx, `
		SELECT r.ci_subject FROM apis a JOIN repos r ON r.id = a.repo_id WHERE a.id = $1`,
		apiID).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return subject, err
}

// Version is a stored version, without its reports.
type Version struct {
	ID          int64
	APIID       string
	Semver      string
	Prerelease  bool
	ContentHash string
	Status      string
	Source      Source
	CreatedAt   time.Time
}

// Source records where a version was pushed from.
type Source struct {
	Repo     string    `json:"repo"`
	Commit   string    `json:"commit_sha,omitempty"`
	Ref      string    `json:"ref,omitempty"`
	CIRunURL string    `json:"ci_run_url,omitempty"`
	PushedBy string    `json:"pushed_by"`
	PushedAt time.Time `json:"pushed_at"`
}

const versionCols = `id, api_id, semver, prerelease, content_hash, status, source, created_at`

func scanVersion(row pgx.Row) (*Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.APIID, &v.Semver, &v.Prerelease, &v.ContentHash, &v.Status, &v.Source, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// PublishedVersion returns the published version of the API with that
// semver, or nil.
func (s *Store) PublishedVersion(ctx context.Context, apiID, semver string) (*Version, error) {
	return scanVersion(s.pool.QueryRow(ctx, `SELECT `+versionCols+` FROM versions
		WHERE api_id = $1 AND semver = $2 AND status = 'published'`, apiID, semver))
}

// LatestPublished returns the published, non-pre-release version with the
// highest semver, or nil. It is the baseline for the next push.
func (s *Store) LatestPublished(ctx context.Context, apiID string) (*Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+versionCols+` FROM versions
		WHERE api_id = $1 AND status = 'published' AND NOT prerelease`, apiID)
	if err != nil {
		return nil, err
	}
	vs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Version, error) { return scanVersion(r) })
	if err != nil {
		return nil, err
	}
	var best *Version
	for _, v := range vs {
		if best == nil || semver.Compare(canonical(v.Semver), canonical(best.Semver)) > 0 {
			best = v
		}
	}
	return best, nil
}

// canonical adds the "v" that golang.org/x/mod/semver requires.
func canonical(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// Bundle returns the packed bundle with the content hash, or nil.
func (s *Store) Bundle(ctx context.Context, contentHash string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM bundles WHERE content_hash = $1`, contentHash).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

// API is the descriptor-level metadata of an API, updated by every push.
type API struct {
	ID        string
	Kind      string
	Owner     string
	Lifecycle string
	Sunset    string // YYYY-MM-DD, or empty
	Meta      map[string]any
}

// Push is one API's outcome to record.
type Push struct {
	API     API
	RepoID  int64
	Actor   string // who pushed, for the audit log
	Version Version
	Bundle  []byte // the packed tar.zst; stored once per content hash
	Acks    map[string]string

	RulesetHash string
	Score       int
	Findings    []model.Finding

	// BaselineVersion is empty when the version wasn't diffed.
	BaselineVersion string
	Changes         []model.Change
}

// Record stores a push in one transaction: it claims the API for the repo if
// it is unclaimed, updates its metadata, and inserts the bundle, version,
// reports and audit entry. A rejected push updates no metadata. It returns
// ErrClaimed if another repo owns the API, and ErrVersionExists if the
// version is published already.
func (s *Store) Record(ctx context.Context, p Push) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := claim(ctx, tx, p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO bundles (content_hash, data, size) VALUES ($1, $2, $3)
			ON CONFLICT (content_hash) DO NOTHING`,
			p.Version.ContentHash, p.Bundle, len(p.Bundle)); err != nil {
			return err
		}
		acks := p.Acks
		if acks == nil {
			acks = map[string]string{}
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO versions (api_id, semver, prerelease, content_hash, status, source, acks)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			p.API.ID, p.Version.Semver, p.Version.Prerelease, p.Version.ContentHash,
			p.Version.Status, p.Version.Source, acks).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrVersionExists
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO lint_reports (version_id, ruleset_hash, score, findings) VALUES ($1, $2, $3, $4)`,
			id, p.RulesetHash, p.Score, nonNil(p.Findings)); err != nil {
			return err
		}
		if p.BaselineVersion != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO diff_reports (version_id, baseline_version, changes) VALUES ($1, $2, $3)`,
				id, p.BaselineVersion, nonNil(p.Changes)); err != nil {
				return err
			}
		}
		return audit(ctx, tx, p.Actor, "push."+p.Version.Status, p.API.ID+"@"+p.Version.Semver,
			map[string]any{"content_hash": p.Version.ContentHash, "source": p.Version.Source})
	})
	return id, err
}

// claim inserts the API or, if this repo owns it, updates its metadata on a
// published push. The row lock taken here serialises pushes of one API.
func claim(ctx context.Context, tx pgx.Tx, p Push) error {
	meta := p.API.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	var sunset *string
	if p.API.Sunset != "" {
		sunset = &p.API.Sunset
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO apis (id, kind, owner, lifecycle, sunset, repo_id, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			kind = CASE WHEN $8 THEN excluded.kind ELSE apis.kind END,
			owner = CASE WHEN $8 THEN excluded.owner ELSE apis.owner END,
			lifecycle = CASE WHEN $8 THEN excluded.lifecycle ELSE apis.lifecycle END,
			sunset = CASE WHEN $8 THEN excluded.sunset ELSE apis.sunset END,
			meta = CASE WHEN $8 THEN excluded.meta ELSE apis.meta END
		WHERE apis.repo_id = excluded.repo_id`,
		p.API.ID, p.API.Kind, p.API.Owner, p.API.Lifecycle, sunset, p.RepoID, meta,
		p.Version.Status == StatusPublished)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrClaimed
	}
	return nil
}

func audit(ctx context.Context, tx pgx.Tx, actor, action, target string, details any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, target, details) VALUES ($1, $2, $3, $4)`,
		actor, action, target, details)
	return err
}

// Audit records an action outside a push, such as a rejected claim.
func (s *Store) Audit(ctx context.Context, actor, action, target string, details any) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return audit(ctx, tx, actor, action, target, details)
	})
}

// Reports returns the findings and changes stored with a version.
func (s *Store) Reports(ctx context.Context, versionID int64) (score int, findings []model.Finding, changes []model.Change, err error) {
	var fs, cs json.RawMessage
	err = s.pool.QueryRow(ctx, `
		SELECT l.score, l.findings, d.changes FROM lint_reports l
		LEFT JOIN diff_reports d ON d.version_id = l.version_id
		WHERE l.version_id = $1`, versionID).Scan(&score, &fs, &cs)
	if err != nil {
		return 0, nil, nil, err
	}
	if err := json.Unmarshal(fs, &findings); err != nil {
		return 0, nil, nil, fmt.Errorf("findings of version %d: %w", versionID, err)
	}
	if cs != nil {
		if err := json.Unmarshal(cs, &changes); err != nil {
			return 0, nil, nil, fmt.Errorf("changes of version %d: %w", versionID, err)
		}
	}
	return score, findings, changes, nil
}

// nonNil keeps an empty report as [] rather than null in jsonb.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
