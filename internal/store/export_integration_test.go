//go:build integration

package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ExplainSearch returns the plan of Search's query, for the benchmark's log.
func (s *Store) ExplainSearch(ctx context.Context, sq SearchQuery) (string, error) {
	var lines []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, searchSettings, wordSimilarity); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+searchSQL, sq.args()...)
		if err != nil {
			return err
		}
		lines, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return strings.Join(lines, "\n"), err
}
