package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/mod/semver"

	"better-api-portal/internal/index"
	"better-api-portal/internal/model"
)

// writeIndex stores a published version's index rows.
func writeIndex(ctx context.Context, tx pgx.Tx, versionID int64, apiID string, v *index.Version) error {
	model, err := json.Marshal(v.Model)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO version_models (version_id, model) VALUES ($1, $2)`, versionID, model); err != nil {
		return err
	}
	copyRows := func(table string, cols []string, n int, row func(i int) []any) error {
		if n == 0 {
			return nil
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromSlice(n, func(i int) ([]any, error) {
			return append([]any{versionID, apiID}, row(i)...), nil
		}))
		return err
	}
	if err := copyRows("messages", []string{"version_id", "api_id", "type", "role", "summary", "deprecated"},
		len(v.Messages), func(i int) []any {
			m := v.Messages[i]
			return []any{m.Type, m.Role, m.Summary, m.Deprecated}
		}); err != nil {
		return err
	}
	if err := copyRows("operations", []string{"version_id", "api_id", "method", "path", "operation_id", "summary", "deprecated"},
		len(v.Operations), func(i int) []any {
			o := v.Operations[i]
			return []any{o.Method, o.Path, o.OperationID, o.Summary, o.Deprecated}
		}); err != nil {
		return err
	}
	if err := copyRows("bindings", []string{"version_id", "api_id", "type", "role", "protocol", "address"},
		len(v.Bindings), func(i int) []any {
			b := v.Bindings[i]
			return []any{b.Type, b.Role, b.Protocol, b.Address}
		}); err != nil {
		return err
	}
	return copyRows("search_docs", []string{"version_id", "api_id", "kind", "ref", "title", "terms", "body"},
		len(v.Docs), func(i int) []any {
			d := v.Docs[i]
			return []any{d.Kind, d.Ref, d.Title, d.Terms, d.Body}
		})
}

// clearIndex removes a version's index rows.
func clearIndex(ctx context.Context, tx pgx.Tx, versionID int64) error {
	for _, table := range []string{"version_models", "messages", "operations", "bindings", "search_docs"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE version_id = $1`, versionID); err != nil {
			return err
		}
	}
	return nil
}

// updateLatest points the API at the version the UI shows by default: the
// highest published non-pre-release, else the highest pre-release.
func updateLatest(ctx context.Context, tx pgx.Tx, apiID string) error {
	rows, err := tx.Query(ctx, `SELECT id, semver, prerelease FROM versions WHERE api_id = $1 AND status = 'published'`, apiID)
	if err != nil {
		return err
	}
	type v struct {
		id         int64
		semver     string
		prerelease bool
	}
	vs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (v, error) {
		var x v
		err := r.Scan(&x.id, &x.semver, &x.prerelease)
		return x, err
	})
	if err != nil {
		return err
	}
	var best *v
	for i := range vs {
		c := &vs[i]
		switch {
		case best == nil:
		case best.prerelease != c.prerelease:
			if c.prerelease {
				continue
			}
		case semver.Compare(canonical(c.semver), canonical(best.semver)) <= 0:
			continue
		}
		best = c
	}
	var id *int64
	if best != nil {
		id = &best.id
	}
	_, err = tx.Exec(ctx, `UPDATE apis SET latest_version_id = $2 WHERE id = $1`, apiID, id)
	return err
}

// syncDependencies rewrites the API's dependencies from the consumes in its
// metadata.
func syncDependencies(ctx context.Context, tx pgx.Tx, apiID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM dependencies WHERE from_api = $1`, apiID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO dependencies (from_api, to_api, types)
		SELECT a.id, c->>'api', ARRAY(SELECT jsonb_array_elements_text(COALESCE(c->'types', '[]')))
		FROM apis a, jsonb_array_elements(COALESCE(a.meta->'consumes', '[]')) c
		WHERE a.id = $1 AND c->>'api' IS NOT NULL AND c->>'api' <> a.id
		ON CONFLICT (from_api, to_api) DO UPDATE SET
			types = ARRAY(SELECT DISTINCT unnest(dependencies.types || excluded.types) ORDER BY 1)`, apiID)
	return err
}

// Indexed is a published version to reindex.
type Indexed struct {
	VersionID   int64
	API         index.API
	Semver      string
	ContentHash string
}

// PublishedVersions lists every published version with its API's current
// title and tags, oldest first.
func (s *Store) PublishedVersions(ctx context.Context) ([]Indexed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.id, v.api_id, v.semver, v.content_hash,
		       COALESCE(a.meta->>'title', ''),
		       ARRAY(SELECT jsonb_array_elements_text(COALESCE(a.meta->'tags', '[]')))
		FROM versions v JOIN apis a ON a.id = v.api_id
		WHERE v.status = 'published'
		ORDER BY v.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Indexed, error) {
		var x Indexed
		err := r.Scan(&x.VersionID, &x.API.ID, &x.Semver, &x.ContentHash, &x.API.Title, &x.API.Tags)
		return x, err
	})
}

// Reindex replaces a published version's index rows.
func (s *Store) Reindex(ctx context.Context, versionID int64, v *index.Version) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var apiID string
		if err := tx.QueryRow(ctx, `SELECT api_id FROM versions WHERE id = $1 AND status = 'published'`, versionID).Scan(&apiID); err != nil {
			return err
		}
		if err := clearIndex(ctx, tx, versionID); err != nil {
			return err
		}
		return writeIndex(ctx, tx, versionID, apiID, v)
	})
}

// RefreshAPIs recomputes every API's latest version and dependencies.
func (s *Store) RefreshAPIs(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM apis ORDER BY id`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := updateLatest(ctx, tx, id); err != nil {
				return err
			}
			if err := syncDependencies(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Model returns a published version's parsed spec, or nil if it isn't
// indexed.
func (s *Store) Model(ctx context.Context, versionID int64) (*model.Spec, error) {
	var spec model.Spec
	err := s.pool.QueryRow(ctx, `SELECT model FROM version_models WHERE version_id = $1`, versionID).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &spec, nil
}

// MessageRole is an API that declares a message type in its latest version.
type MessageRole struct {
	APIID, Role, Semver string
	Deprecated          bool
	// VersionID and ContentHash locate the version's model and bundle.
	VersionID   int64
	ContentHash string
}

// MessageRoles lists the APIs whose latest version declares the message
// type, with their role (J4: who produces and who receives it).
func (s *Store) MessageRoles(ctx context.Context, msgType string) ([]MessageRole, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.api_id, m.role, v.semver, m.deprecated, v.id, v.content_hash
		FROM messages m
		JOIN apis a ON a.latest_version_id = m.version_id
		JOIN versions v ON v.id = m.version_id
		WHERE m.type = $1
		ORDER BY m.role, m.api_id`, msgType)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (MessageRole, error) {
		var x MessageRole
		err := r.Scan(&x.APIID, &x.Role, &x.Semver, &x.Deprecated, &x.VersionID, &x.ContentHash)
		return x, err
	})
}

// TypeConsumers lists the APIs that declare they consume the message type:
// by naming it, or by consuming the whole of an API in owners, the APIs
// that declare the type.
func (s *Store) TypeConsumers(ctx context.Context, msgType string, owners []string) ([]Dependency, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT from_api, to_api, types FROM dependencies
		WHERE $1 = ANY(types) OR (cardinality(types) = 0 AND to_api = ANY($2))
		ORDER BY from_api, to_api`, msgType, owners)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Dependency, error) {
		var d Dependency
		err := r.Scan(&d.From, &d.To, &d.Types)
		return d, err
	})
}

// Dependency is one API's declared use of another.
type Dependency struct {
	From, To string
	Types    []string // empty means the whole API
}

// Dependencies lists what the API consumes and who consumes it.
func (s *Store) Dependencies(ctx context.Context, apiID string) (consumes, consumers []Dependency, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT from_api, to_api, types FROM dependencies
		WHERE from_api = $1 OR to_api = $1 ORDER BY from_api, to_api`, apiID)
	if err != nil {
		return nil, nil, err
	}
	deps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Dependency, error) {
		var d Dependency
		err := r.Scan(&d.From, &d.To, &d.Types)
		return d, err
	})
	if err != nil {
		return nil, nil, err
	}
	for _, d := range deps {
		if d.From == apiID {
			consumes = append(consumes, d)
		} else {
			consumers = append(consumers, d)
		}
	}
	return consumes, consumers, nil
}

// Hit is a search result.
type Hit struct {
	APIID, Semver, Kind, Ref, Title string
	Rank                            float32
}

// Search finds documents in the APIs' latest versions matching q, a
// web-search style query ("refund -legacy"), best first. It is the basic
// full-text query; M3's search step adds trigram matching on titles and
// ranking by lifecycle.
func (s *Store) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.api_id, v.semver, d.kind, d.ref, d.title, ts_rank(d.tsv, query) AS rank
		FROM search_docs d
		JOIN apis a ON a.latest_version_id = d.version_id
		JOIN versions v ON v.id = d.version_id,
		     websearch_to_tsquery('english', $1) query
		WHERE d.tsv @@ query
		ORDER BY rank DESC, d.api_id, d.kind, d.ref
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hit, error) {
		var h Hit
		err := r.Scan(&h.APIID, &h.Semver, &h.Kind, &h.Ref, &h.Title, &h.Rank)
		return h, err
	})
}
