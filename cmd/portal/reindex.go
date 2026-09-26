package main

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/check"
	"better-api-portal/internal/index"
	"better-api-portal/internal/store"
)

func reindexCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild the browse and search index from the stored bundles",
		Long: `Rebuild what the UI and search read, for every published version, from the
stored bundles: the parsed model, the message, operation and binding rows,
and the search documents; then each API's latest version and dependencies.
Run it after an upgrade that changes the index. Pushes can continue
meanwhile. A version that fails is reported and skipped; the command then
exits non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dsn, err := dsnOrEnv(dsn)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := store.Open(ctx, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := s.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			versions, err := s.PublishedVersions(ctx)
			if err != nil {
				return err
			}
			log := cmd.ErrOrStderr()
			failed := 0
			for _, v := range versions {
				if err := reindexOne(cmd, s, v); err != nil {
					failed++
					fmt.Fprintf(log, "%s %s: %v\n", v.API.ID, v.Semver, err)
				}
			}
			if err := s.RefreshAPIs(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "reindexed %d of %d published version(s)\n", len(versions)-failed, len(versions))
			if failed > 0 {
				return fmt.Errorf("%d version(s) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "Postgres connection string (default $PORTAL_DSN)")
	return cmd
}

func reindexOne(cmd *cobra.Command, s *store.Store, v store.Indexed) error {
	data, err := s.Bundle(cmd.Context(), v.ContentHash)
	if err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("bundle %s is missing", v.ContentHash)
	}
	b, err := bundle.Unpack(bytes.NewReader(data))
	if err != nil {
		return err
	}
	spec, err := check.ParseBundle(b)
	if err != nil {
		return err
	}
	return s.Reindex(cmd.Context(), v.VersionID, index.Build(v.API, spec))
}
