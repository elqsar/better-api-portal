// Command portal is the API portal: CLI for CI and, later, the server.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/check"
	"better-api-portal/internal/compat"
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/model"
	"better-api-portal/internal/report"
	"better-api-portal/internal/store"
	"better-api-portal/internal/yamldoc"
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
	root.AddCommand(checkCmd(), diffCmd(), bundleCmd(), migrateCmd())
	return root
}

func checkCmd() *cobra.Command {
	var (
		descPath   string
		configPath string
		baselines  []string
		acks       []string
		ackReason  string
		format     string
		outputs    []string
		strict     bool
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
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			renderers := map[string]func(io.Writer, *check.Report) error{
				"text":  report.Text,
				"json":  report.JSON,
				"sarif": func(w io.Writer, r *check.Report) error { return report.SARIF(w, r, root) },
				"junit": func(w io.Writer, r *check.Report) error { return report.JUnit(w, r, strict) },
			}
			render, ok := renderers[format]
			if !ok {
				return fmt.Errorf("unknown --format %q (want text, json, sarif or junit)", format)
			}
			type output struct{ format, path string }
			var files []output
			seen := map[string]bool{}
			for _, o := range outputs {
				f, p, _ := strings.Cut(o, "=")
				if _, ok := renderers[f]; !ok || p == "" {
					return fmt.Errorf("--output %q: want format=path, with format text, json, sarif or junit", o)
				}
				abs, err := filepath.Abs(p)
				if err != nil {
					return err
				}
				if seen[abs] {
					return fmt.Errorf("--output %q: %s is already an output", o, p)
				}
				seen[abs] = true
				files = append(files, output{f, p})
			}
			opts := check.Options{Baselines: baselines}
			if len(acks) > 0 {
				if strings.TrimSpace(ackReason) == "" {
					return errors.New("--ack needs --ack-reason: say why the breaking change is safe")
				}
				opts.Acks = map[string]string{}
				for _, id := range acks {
					opts.Acks[id] = ackReason
				}
			}
			if configPath != "" {
				cfg, err := config.Load(configPath)
				if err != nil {
					return err
				}
				opts.Config = cfg
			}
			r, err := check.Run(descPath, opts)
			if err != nil {
				return err
			}
			if err := render(cmd.OutOrStdout(), r); err != nil {
				return err
			}
			for _, o := range files {
				var buf bytes.Buffer
				if err := renderers[o.format](&buf, r); err != nil {
					return err
				}
				if err := os.WriteFile(o.path, buf.Bytes(), 0o644); err != nil {
					return err
				}
			}
			c := model.Counts(r.Findings)
			if c[model.SeverityError] > 0 || (strict && c[model.SeverityWarn] > 0) {
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
	cmd.Flags().StringVar(&format, "format", "text", "stdout format: text, json, sarif or junit")
	cmd.Flags().StringArrayVar(&outputs, "output", nil, "also write format=path, e.g. sarif=portal.sarif (repeatable)")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail on warnings too")
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
			bundles := map[string]*bundle.Bundle{}
			if d != nil {
				for i, api := range d.APIs {
					if !d.SpecOK(i) {
						continue // already a finding
					}
					c, problems, err := bundle.Load(d.Dir, d.SpecPath(i))
					var se *yamldoc.SyntaxError
					if errors.As(err, &se) {
						findings = append(findings, model.Finding{API: api.ID, RuleID: "bundle-syntax", Severity: model.SeverityError,
							File: d.SpecPath(i), Line: se.Line, Message: "not valid YAML or JSON: " + se.Error()})
						continue
					}
					if err != nil {
						return err
					}
					for _, p := range problems {
						findings = append(findings, model.Finding{API: api.ID, RuleID: "bundle-ref", Severity: model.SeverityError,
							File: p.File, Pointer: p.Pointer, Line: p.Line, Message: p.Message})
					}
					if len(problems) > 0 {
						continue
					}
					b, err := bundle.Read(c.Root, c.Files())
					if err != nil {
						return err
					}
					bundles[api.ID] = b
				}
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
			if dsn == "" {
				dsn = os.Getenv("PORTAL_DSN")
			}
			if dsn == "" {
				return errors.New("no database: pass --dsn or set PORTAL_DSN")
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
