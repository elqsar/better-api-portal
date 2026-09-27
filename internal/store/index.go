package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
	// The API's kind (openapi, cloudevents, …), owner and lifecycle.
	APIKind, Owner, Lifecycle string
	// Snippet is an excerpt of the document's text, with the matched words
	// between SnippetStart and SnippetStop. It isn't HTML.
	Snippet string
	Rank    float32
}

// Snippet delimiters: control characters, so no markup comes out of
// Postgres.
const (
	SnippetStart = "\x01"
	SnippetStop  = "\x02"
)

// SearchQuery is a search. Empty filters match everything.
type SearchQuery struct {
	// Q is a web-search style query ("refund -legacy"). Its titles are
	// matched by trigram as well, so a typo still finds them.
	Q               string
	Kind, Team, API string
	Limit           int
}

// wordSimilarity is the trigram threshold: 0.6 by default, which misses
// "refnd" for "refund" (0.5).
const wordSimilarity = "0.45"

// searchSettings applies to Search's transaction. pgx prepares the query,
// and after five runs Postgres may switch to a generic plan, which can't
// drop the UNION branches that don't apply to the query: at 500 APIs that
// took p95 from 61 ms to 6 s.
const searchSettings = `SELECT set_config('pg_trgm.word_similarity_threshold', $1, true),
	set_config('plan_cache_mode', 'force_custom_plan', true)`

// Search finds documents in the APIs' latest versions, best first. A
// document matches by full text (terms above body) or by trigram on its
// title. Deprecated APIs rank at half weight; retired ones are left out.
func (s *Store) Search(ctx context.Context, sq SearchQuery) ([]Hit, error) {
	if strings.TrimSpace(sq.Q) == "" {
		return nil, nil
	}
	var hits []Hit
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, searchSettings, wordSimilarity); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, searchSQL, sq.args()...)
		if err != nil {
			return err
		}
		hits, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hit, error) {
			var h Hit
			err := r.Scan(&h.APIID, &h.Semver, &h.Kind, &h.Ref, &h.Title, &h.APIKind, &h.Owner, &h.Lifecycle, &h.Snippet, &h.Rank)
			return h, err
		})
		return err
	})
	return hits, err
}

func (sq SearchQuery) args() []any {
	return []any{sq.Q, escapeLike(sq.Q), sq.Kind, sq.Team, sq.API, sq.Limit, SnippetStart, SnippetStop}
}

// searchSQL runs with pg_trgm.word_similarity_threshold set. The
// candidates are a UNION of the index-backed matches: an OR would use
// neither GIN index. Short queries have too few trigrams, so they use a
// substring match instead. Snippets are made for the returned rows only.
const searchSQL = `
	WITH q AS (
		SELECT websearch_to_tsquery('english', $1) AS tsq, $1::text AS raw
	),
	candidates AS (
		SELECT d.id FROM search_docs d, q WHERE d.tsv @@ q.tsq
		UNION
		SELECT d.id FROM search_docs d, q WHERE length(q.raw) >= 3 AND d.title %> q.raw
		UNION
		SELECT d.id FROM search_docs d, q WHERE length(q.raw) < 3 AND d.title ILIKE '%' || $2 || '%'
	),
	ranked AS (
		SELECT d.id, d.api_id, v.semver, d.kind, d.ref, d.title, d.body,
		       a.kind AS api_kind, a.owner, a.lifecycle,
		       ((ts_rank_cd(d.tsv, q.tsq) + 2 * word_similarity(q.raw, d.title)
		         + CASE WHEN lower(d.title) = lower(q.raw) THEN 1 ELSE 0 END
		         + CASE d.kind WHEN 'api' THEN 0.1 ELSE 0 END)
		        * CASE a.lifecycle WHEN 'deprecated' THEN 0.5 ELSE 1 END)::real AS rank
		FROM candidates c
		JOIN search_docs d ON d.id = c.id
		JOIN apis a ON a.latest_version_id = d.version_id
		JOIN versions v ON v.id = d.version_id,
		     q
		WHERE a.lifecycle <> 'retired'
		  AND ($3 = '' OR d.kind = $3)
		  AND ($4 = '' OR a.owner = $4)
		  AND ($5 = '' OR d.api_id = $5)
		ORDER BY rank DESC, d.api_id, d.kind, d.ref
		LIMIT $6
	)
	SELECT r.api_id, r.semver, r.kind, r.ref, r.title, r.api_kind, r.owner, r.lifecycle,
	       CASE WHEN r.body = '' THEN ''
	            ELSE ts_headline('english', r.body, q.tsq,
	                             'StartSel=' || $7 || ', StopSel=' || $8 || ', MaxWords=24, MinWords=12')
	       END,
	       r.rank
	FROM ranked r, q
	ORDER BY r.rank DESC, r.api_id, r.kind, r.ref`
