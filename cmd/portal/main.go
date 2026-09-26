// Command portal is the API portal: CLI for CI and, later, the server.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"better-api-portal/internal/check"
	"better-api-portal/internal/compat"
	"better-api-portal/internal/config"
	"better-api-portal/internal/model"
	"better-api-portal/internal/report"
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
	root.AddCommand(checkCmd(), diffCmd())
	return root
}

func checkCmd() *cobra.Command {
	var (
		descPath   string
		configPath string
		baseline   string
		acks       []string
		ackReason  string
		format     string
		strict     bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the descriptor and its specs locally; writes nothing",
		Long: `Validate the descriptor and its specs, lint them, and, given --baseline,
diff each API against the previous version and apply the versioning policy.

--baseline takes the previous version's portal.yaml, for example from
  git worktree add ../base main
Event catalogues are diffed natively and OpenAPI with oasdiff. A breaking
change without a major bump fails with an id (BRK-CE-… or BRK-OA-…) that
--ack accepts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			render, ok := map[string]func(io.Writer, *check.Report) error{
				"text": report.Text,
				"json": report.JSON,
			}[format]
			if !ok {
				return fmt.Errorf("unknown --format %q (want text or json)", format)
			}
			opts := check.Options{Baseline: baseline}
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
			c := model.Counts(r.Findings)
			if c[model.SeverityError] > 0 || (strict && c[model.SeverityWarn] > 0) {
				return errFindings
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&descPath, "descriptor", "portal.yaml", "path to the descriptor")
	cmd.Flags().StringVar(&configPath, "config", "", "portal.config.yaml, for org rules such as the event type prefix")
	cmd.Flags().StringVar(&baseline, "baseline", "", "the previous version's portal.yaml, to diff against")
	cmd.Flags().StringArrayVar(&acks, "ack", nil, "acknowledge a breaking change by id (repeatable)")
	cmd.Flags().StringVar(&ackReason, "ack-reason", "", "why the acknowledged changes are safe (required with --ack)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
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
