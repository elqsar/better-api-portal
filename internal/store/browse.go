package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/elqsar/better-api-portal/internal/model"
)

// APISummary is an API in the list: its current metadata and latest
// version.
type APISummary struct {
	ID, Kind, Title, Owner, Lifecycle string
	Sunset                            *time.Time
	Tags                              []string
	Latest                            string
	LatestAt                          time.Time
	Score                             int
	// Description is the latest version's spec description.
	Description string
}

// APIFilter narrows the API list; empty fields match everything.
type APIFilter struct {
	Team, Kind, Lifecycle, Tag string
	// Q matches the id or title, case-insensitively.
	Q string
}

// titleSQL is an API's title: the descriptor's, else the latest version's
// spec title (vm is its version_models row).
const titleSQL = `COALESCE(NULLIF(a.meta->>'title', ''), vm.model->>'title', '')`

// ListAPIs lists the APIs with a published version: in use first, then
// deprecated, then retired, each by id.
func (s *Store) ListAPIs(ctx context.Context, f APIFilter) ([]APISummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.kind, `+titleSQL+`, a.owner, a.lifecycle, a.sunset,
		       ARRAY(SELECT jsonb_array_elements_text(COALESCE(a.meta->'tags', '[]'))),
		       v.semver, v.created_at, COALESCE(l.score, 0), COALESCE(vm.model->>'description', '')
		FROM apis a
		JOIN versions v ON v.id = a.latest_version_id
		LEFT JOIN version_models vm ON vm.version_id = v.id
		LEFT JOIN lint_reports l ON l.version_id = v.id
		WHERE ($1 = '' OR a.owner = $1)
		  AND ($2 = '' OR a.kind = $2)
		  AND ($3 = '' OR a.lifecycle = $3)
		  AND ($4 = '' OR COALESCE(a.meta->'tags', '[]') ? $4)
		  AND ($5 = '' OR a.id ILIKE '%' || $5 || '%' OR `+titleSQL+` ILIKE '%' || $5 || '%')
		ORDER BY CASE a.lifecycle WHEN 'deprecated' THEN 1 WHEN 'retired' THEN 2 ELSE 0 END, a.id`,
		f.Team, f.Kind, f.Lifecycle, f.Tag, escapeLike(f.Q))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (APISummary, error) {
		var a APISummary
		err := r.Scan(&a.ID, &a.Kind, &a.Title, &a.Owner, &a.Lifecycle, &a.Sunset, &a.Tags, &a.Latest, &a.LatestAt, &a.Score, &a.Description)
		return a, err
	})
}

func escapeLike(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

// Tags lists the tags of APIs with a published version.
func (s *Store) Tags(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT t FROM apis a, jsonb_array_elements_text(COALESCE(a.meta->'tags', '[]')) t
		WHERE a.latest_version_id IS NOT NULL ORDER BY t`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Link is a titled URL from the descriptor.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Environment is where an API runs, from the descriptor.
type Environment struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Broker string `json:"broker"`
}

// APIDetail is an API with its descriptor metadata.
type APIDetail struct {
	APIRecord
	LatestSemver string // "" if nothing is published
	Sunset       *time.Time
	Meta
}

// Name is the API's title, or its id if it has none.
func (d *APIDetail) Name() string {
	if d.Title != "" {
		return d.Title
	}
	return d.ID
}

// Meta is the descriptor metadata a push stores on the API (apis.meta).
type Meta struct {
	Title         string        `json:"title"`
	System        string        `json:"system"`
	Tags          []string      `json:"tags"`
	Links         []Link        `json:"links"`
	Environments  []Environment `json:"environments"`
	Compatibility string        `json:"compatibility"`
}

// APIDetail returns the API with the id, or nil.
func (s *Store) APIDetail(ctx context.Context, id string) (*APIDetail, error) {
	var d APIDetail
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.kind, a.owner, a.lifecycle, r.ci_subject, COALESCE(a.latest_version_id, 0),
		       COALESCE(v.semver, ''), a.sunset, a.meta, `+titleSQL+`
		FROM apis a JOIN repos r ON r.id = a.repo_id LEFT JOIN versions v ON v.id = a.latest_version_id
		LEFT JOIN version_models vm ON vm.version_id = v.id
		WHERE a.id = $1`, id).
		Scan(&d.ID, &d.Kind, &d.Owner, &d.Lifecycle, &d.Repo, &d.LatestVersionID, &d.LatestSemver, &d.Sunset, &d.Meta,
			&d.Title) // after Meta, whose title it replaces
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// VersionSummary is a version in an API's history.
type VersionSummary struct {
	Version
	Score            int
	Errors, Warnings int
	BaselineVersion  string
	Breaking         int // breaking changes against the baseline
	Acks             int // acknowledged breaking changes
}

// Versions lists every version of the API, published and rejected, newest
// first.
func (s *Store) Versions(ctx context.Context, apiID string) ([]VersionSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+prefixed("v", versionCols)+`, COALESCE(l.score, 0),
		       (SELECT count(*) FROM jsonb_array_elements(COALESCE(l.findings, '[]')) f WHERE f->>'severity' = 'error'),
		       (SELECT count(*) FROM jsonb_array_elements(COALESCE(l.findings, '[]')) f WHERE f->>'severity' = 'warn'),
		       COALESCE(d.baseline_version, ''),
		       (SELECT count(*) FROM jsonb_array_elements(COALESCE(d.changes, '[]')) c WHERE c->>'impact' = 'breaking'),
		       (SELECT count(*) FROM jsonb_object_keys(v.acks))
		FROM versions v
		LEFT JOIN lint_reports l ON l.version_id = v.id
		LEFT JOIN diff_reports d ON d.version_id = v.id
		WHERE v.api_id = $1
		ORDER BY v.created_at DESC, v.id DESC`, apiID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (VersionSummary, error) {
		var x VersionSummary
		err := r.Scan(&x.ID, &x.APIID, &x.Semver, &x.Prerelease, &x.ContentHash, &x.Status, &x.Source, &x.CreatedAt,
			&x.Score, &x.Errors, &x.Warnings, &x.BaselineVersion, &x.Breaking, &x.Acks)
		return x, err
	})
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// Report is what the pipeline said about a version.
type Report struct {
	Score           int
	Findings        []model.Finding
	BaselineVersion string // "" if it wasn't diffed
	Changes         []model.Change
	Acks            map[string]string // change id → reason
}

// Report returns a version's lint and diff reports and its acks.
func (s *Store) Report(ctx context.Context, versionID int64) (*Report, error) {
	var r Report
	var baseline *string
	err := s.pool.QueryRow(ctx, `
		SELECT l.score, l.findings, d.baseline_version, COALESCE(d.changes, '[]'), v.acks
		FROM versions v
		JOIN lint_reports l ON l.version_id = v.id
		LEFT JOIN diff_reports d ON d.version_id = v.id
		WHERE v.id = $1`, versionID).Scan(&r.Score, &r.Findings, &baseline, &r.Changes, &r.Acks)
	if err != nil {
		return nil, err
	}
	if baseline != nil {
		r.BaselineVersion = *baseline
	}
	return &r, nil
}
