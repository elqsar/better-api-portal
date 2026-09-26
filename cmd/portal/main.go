// Command portal is the API portal: CLI for CI and, later, the server.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/bundle"
	"better-api-portal/internal/check"
	"better-api-portal/internal/compat"
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/httpapi"
	"better-api-portal/internal/model"
	"better-api-portal/internal/report"
	"better-api-portal/internal/store"
)

// Exit codes: 1 means the check found problems, 2 that it couldn't run.
const (
	exitFindings = 1
	exitFailure  = 2
)

// errFindings signals exit code 1 after the report has been printed.
var errFindings = errors.New("check failed")

func main() {
	err := newRoot().Execute()
	switch {
	case err == nil:
	case errors.Is(err, errFindings):
		os.Exit(exitFindings)
	default:
		fmt.Fprintln(os.Stderr, "portal:", err)
		os.Exit(exitFailure)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "portal",
		Short:         "Catalogue and govern HTTP and event APIs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(checkCmd(), diffCmd(), bundleCmd(), migrateCmd(), serveCmd(), adminCmd(), pushCmd())
	return root
}

func checkCmd() *cobra.Command {
	var (
		descPath     string
		configPath   string
		baselines    []string
		acks         []string
		ackReason    string
		baselineFrom string
		audience     string
		reports      reportFlags
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the descriptor and its specs locally; writes only the reports asked for",
		Long: `Validate the descriptor and its specs, lint them, and, given --baseline,
diff each API against the previous version and apply the versioning policy.

--baseline takes the previous version's portal.yaml, for example from
  git worktree add ../base main
or, per API, a bundle written by portal bundle: --baseline orders-http=orders-http.tar.zst.
It can be repeated: at most one portal.yaml, plus bundles, which take precedence.
--baseline-from URL fetches each API's latest published version from the
portal instead, as a push would diff against it; an API with none isn't
diffed. It authenticates like portal push.
Event catalogues are diffed natively and OpenAPI with oasdiff. A breaking
change without a major bump fails with an id (BRK-CE-… or BRK-OA-…) that
--ack accepts.

--format picks what goes to stdout: text, json, sarif or junit. --output
format=path writes the same result to a file as well, and can be repeated, so
one run gives a readable log plus files for CI. In GitHub Actions:
  portal check --baseline ../base/portal.yaml --output sarif=portal.sarif
then upload portal.sarif with github/codeql-action/upload-sarif (if: always()).
The files are written even when the check fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := reports.prepare()
			if err != nil {
				return err
			}
			opts := check.Options{Baselines: baselines}
			if opts.Acks, err = ackMap(acks, ackReason); err != nil {
				return err
			}
			if configPath != "" {
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				opts.Config = cfg
			}
			if baselineFrom != "" {
				if opts.BaselineBundles, err = fetchBaselines(cmd.Context(), cmd.ErrOrStderr(), descPath, baselineFrom, audience); err != nil {
					return err
				}
			}
			r, err := check.Run(descPath, opts)
			if err != nil {
				return err
			}
			if err := out.write(cmd.OutOrStdout(), r); err != nil {
				return err
			}
			c := model.Counts(r.Findings)
			if c[model.SeverityError] > 0 || (reports.strict && c[model.SeverityWarn] > 0) {
				return errFindings
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&descPath, "descriptor", "portal.yaml", "path to the descriptor")
	cmd.Flags().StringVar(&configPath, "config", "", "portal.config.yaml, for org rules such as the event type prefix")
	cmd.Flags().StringArrayVar(&baselines, "baseline", nil, "the previous version's portal.yaml, or api-id=bundle.tar.zst (repeatable)")
	cmd.Flags().StringArrayVar(&acks, "ack", nil, "acknowledge a breaking change by id (repeatable)")
	cmd.Flags().StringVar(&ackReason, "ack-reason", "", "why the acknowledged changes are safe (required with --ack)")
	cmd.Flags().StringVar(&baselineFrom, "baseline-from", "", "portal URL: diff each API against its latest published version there")
	cmd.Flags().StringVar(&audience, "audience", config.DefaultAudience, "audience of the GitHub Actions ID token, with --baseline-from")
	reports.flags(cmd)
	cmd.Flags().BoolVar(&reports.strict, "strict", false, "fail on warnings too")
	return cmd
}

func diffCmd() *cobra.Command {
	var (
		mode   string
		format string
	)
	cmd := &cobra.Command{
		Use:   "diff <old-spec> <new-spec>",
		Short: "Compare two spec files; exits 1 if any change is breaking",
		Long: `Compare two spec files of the same kind, an event catalogue or an OpenAPI
document, and exit 1 if any change is breaking. --compatibility applies to
event catalogues only.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m := compat.Mode(strings.ToUpper(mode))
			switch m {
			case "", compat.Forward, compat.Backward, compat.Full, compat.None:
			default:
				return fmt.Errorf("unknown --compatibility %q (want FORWARD, BACKWARD, FULL or NONE)", mode)
			}
			if format != "text" && format != "json" {
				return fmt.Errorf("unknown --format %q (want text or json)", format)
			}
			changes, findings, err := check.DiffFiles(args[0], args[1], m)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if changes == nil && len(findings) > 0 {
				// A spec that can't be parsed can't be compared.
				if err := report.Text(w, &check.Report{Findings: findings}); err != nil {
					return err
				}
				return errFindings
			}
			if format == "json" {
				err = report.ChangesJSON(w, changes)
			} else if len(changes) == 0 {
				_, err = fmt.Fprintln(w, "no changes")
			} else {
				err = report.Changes(w, changes, "")
			}
			if err != nil {
				return err
			}
			for _, c := range changes {
				if c.Impact == model.ImpactBreaking {
					return errFindings
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "compatibility", "", "payload compatibility mode for every message (default: by role)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func bundleCmd() *cobra.Command {
	var descPath, out string
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Pack each API's spec and its $ref'd files into <out>/<api-id>.tar.zst",
		Long: `Pack each API's spec, plus every file it reaches through $ref, into
<out>/<api-id>.tar.zst, and print its content hash. Bundles can be given to
check --baseline as api-id=file. Doesn't lint: run check for that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("--out is required")
			}
			d, findings, err := descriptor.Load(descPath)
			if err != nil {
				return err
			}
			type packed struct{ id, hash, path string }
			var done []packed
			var bundles map[string]*bundle.Bundle
			if d != nil {
				var fs []model.Finding
				if bundles, fs, err = check.BundleAPIs(d); err != nil {
					return err
				}
				findings = append(findings, fs...)
			}
			if model.Counts(findings)[model.SeverityError] > 0 {
				if err := report.Text(cmd.OutOrStdout(), &check.Report{Findings: findings}); err != nil {
					return err
				}
				return errFindings
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			for _, api := range d.APIs {
				b := bundles[api.ID]
				if b == nil {
					continue
				}
				hash, err := b.Hash()
				if err != nil {
					return err
				}
				var buf bytes.Buffer
				if err := b.Pack(&buf); err != nil {
					return err
				}
				p := filepath.Join(out, api.ID+".tar.zst")
				if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
					return err
				}
				done = append(done, packed{api.ID, hash, p})
			}
			for _, p := range done {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", p.id, p.hash, p.path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&descPath, "descriptor", "portal.yaml", "path to the descriptor")
	cmd.Flags().StringVar(&out, "out", "", "directory to write the bundles to (created if missing)")
	return cmd
}

func serveCmd() *cobra.Command {
	var dsn, configPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the portal server",
		Long: `Run the REST API. It migrates the database at startup (behind an advisory
lock, so replicas can start together) and syncs the teams from the
configuration. Logs are JSON on stderr.

CI jobs authenticate with an ID token from an issuer in ci.trustedIssuers
(GitHub Actions, GitLab), or with a static token from portal admin token
create.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dsn, err := dsnOrEnv(dsn)
			if err != nil {
				return err
			}
			cfg := &config.Config{}
			if configPath != "" {
				if cfg, err = config.Load(configPath); err != nil {
					return err
				}
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			s, err := store.Open(ctx, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := s.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			teams := make([]store.Team, len(cfg.Teams))
			for i, t := range cfg.Teams {
				teams[i] = store.Team{Slug: t.Slug, Name: t.Name, OIDCGroup: t.OIDCGroup}
			}
			if err := s.SyncTeams(ctx, teams); err != nil {
				return fmt.Errorf("sync teams: %w", err)
			}

			api := &httpapi.Server{Store: s, Config: cfg, Auth: auth.NewCI(cfg.CI.TrustedIssuers, s, nil), Log: log}
			addr := cfg.Server.Listen
			if addr == "" {
				addr = ":8080"
			}
			srv := &http.Server{
				Addr:              addr,
				Handler:           api.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       2 * time.Minute, // a push can be tens of MB
				WriteTimeout:      2 * time.Minute,
				IdleTimeout:       2 * time.Minute,
			}
			errc := make(chan error, 1)
			go func() { errc <- srv.ListenAndServe() }()
			log.Info("serving", "addr", addr)
			select {
			case err := <-errc:
				return err
			case <-ctx.Done():
			}
			log.Info("shutting down")
			shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "Postgres connection string (default $PORTAL_DSN)")
	cmd.Flags().StringVar(&configPath, "config", "", "portal configuration (portal.config.yaml)")
	return cmd
}

func dsnOrEnv(dsn string) (string, error) {
	if dsn == "" {
		dsn = os.Getenv("PORTAL_DSN")
	}
	if dsn == "" {
		return "", errors.New("no database: pass --dsn or set PORTAL_DSN")
	}
	return dsn, nil
}

func migrateCmd() *cobra.Command {
	var dsn string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations",
		Long: `Apply pending migrations to the portal's Postgres database. portal serve
does this at startup too; the command is for pipelines that prefer an
explicit step. It holds an advisory lock, so concurrent runs are safe.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dsn, err := dsnOrEnv(dsn)
			if err != nil {
				return err
			}
			ctx := context.Background()
			s, err := store.Open(ctx, dsn)
			if err != nil {
				return err
			}
			defer s.Close()
			return s.Migrate(ctx)
		},
	}
	cmd.Flags().StringVar(&dsn, "dsn", "", "Postgres connection string (default $PORTAL_DSN)")
	return cmd
}
