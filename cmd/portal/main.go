// Command portal is the API portal: CLI for CI and, later, the server.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"better-api-portal/internal/descriptor"
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
	root := &cobra.Command{
		Use:           "portal",
		Short:         "Catalogue and govern HTTP and event APIs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(checkCmd())

	err := root.Execute()
	switch {
	case err == nil:
	case errors.Is(err, errFindings):
		os.Exit(exitFindings)
	default:
		fmt.Fprintln(os.Stderr, "portal:", err)
		os.Exit(exitFailure)
	}
}

func checkCmd() *cobra.Command {
	var (
		descPath string
		format   string
		strict   bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the descriptor and its specs locally; writes nothing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			render, ok := map[string]func(w io.Writer, fs []model.Finding) error{
				"text": report.Text,
				"json": report.JSON,
			}[format]
			if !ok {
				return fmt.Errorf("unknown --format %q (want text or json)", format)
			}
			_, findings, err := descriptor.Load(descPath)
			if err != nil {
				return err
			}
			if err := render(cmd.OutOrStdout(), findings); err != nil {
				return err
			}
			c := model.Counts(findings)
			if c[model.SeverityError] > 0 || (strict && c[model.SeverityWarn] > 0) {
				return errFindings
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&descPath, "descriptor", "portal.yaml", "path to the descriptor")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().BoolVar(&strict, "strict", false, "fail on warnings too")
	return cmd
}
