package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/initkit"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/report"
)

const onboardingGuide = "https://github.com/elqsar/better-api-portal/blob/main/docs/guide/onboarding.md"

func initCmd() *cobra.Command {
	var (
		root       string
		opts       initkit.Options
		force      bool
		stdout     bool
		ci         string
		portalURL  string
		cliVersion string
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a portal.yaml for the specs in this repo, and optionally the CI workflow",
		Long: `Find the repo's OpenAPI 3, AsyncAPI 3 and event catalogue files, write a
portal.yaml that publishes each of them, and check it.

APIs are named <service>-http and <service>-events, where the service is the
repository directory's name without a -service, -svc or -api suffix
(--service overrides it). A kind with several specs names each from its
title instead. Ids are permanent once published, so review them.

--ci github also writes ` + initkit.WorkflowPath + `, which checks pull
requests and publishes from main and tags; it needs --portal-url.

Nothing is overwritten without --force; --stdout prints the descriptor
instead of writing it. Guide: ` + onboardingGuide,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Owner == "" {
				return errors.New("--owner is required: the team slug that owns these APIs, as in the portal's configuration")
			}
			if ci != "" && ci != "github" {
				return fmt.Errorf("unknown --ci %q (want github)", ci)
			}
			if ci != "" && portalURL == "" {
				return errors.New("--ci needs --portal-url, the portal the workflow pushes to")
			}
			if ci != "" && cliVersion == "" {
				v := portalVersion()
				if !semver.IsValid(v) || semver.Prerelease(v) != "" || semver.Build(v) != "" {
					return fmt.Errorf("this portal binary (%s) isn't a release, so --cli-version is required with --ci", v)
				}
				cliVersion = v
			}
			w, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

			specs, unsupported, err := initkit.Detect(root)
			if err != nil {
				return err
			}
			for _, f := range unsupported {
				fmt.Fprintf(errw, "skipped %s: %s\n", f.File, f.Message)
			}
			if len(specs) == 0 {
				return fmt.Errorf("no OpenAPI 3, AsyncAPI 3 or event catalogue file found under %s; to describe existing CloudEvents with JSON Schemas, see %s", root, onboardingGuide)
			}
			if opts.Service == "" {
				opts.Service = initkit.ServiceName(root)
			}
			desc := initkit.Descriptor(opts, initkit.Propose(specs, opts.Service))
			if stdout {
				_, err := w.Write(desc)
				return err
			}

			descPath := filepath.Join(root, "portal.yaml")
			files := map[string][]byte{descPath: desc}
			if ci == "github" {
				files[filepath.Join(root, initkit.WorkflowPath)] = initkit.Workflow(portalURL, cliVersion)
			}
			for p := range files {
				if _, err := os.Stat(p); err == nil && !force {
					return fmt.Errorf("%s exists; pass --force to overwrite it", p)
				} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			for _, p := range []string{descPath, filepath.Join(root, initkit.WorkflowPath)} {
				b, ok := files[p]
				if !ok {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, b, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(w, "wrote %s\n", p)
			}

			fmt.Fprintf(w, "\nChecking %s:\n", descPath)
			r, err := check.Run(descPath, check.Options{})
			if err != nil {
				return err
			}
			if err := report.Text(w, r); err != nil {
				return err
			}
			errs := model.Counts(r.Findings)[model.SeverityError]
			fmt.Fprintln(w, "\nNext:")
			if errs > 0 {
				fmt.Fprintln(w, "  - fix the errors above, then rerun portal check")
			}
			fmt.Fprintln(w, "  - review the API ids: they can't change once published")
			if ci == "" {
				fmt.Fprintln(w, "  - add the CI workflow: portal init --ci github --portal-url URL --force, or see the guide")
			}
			fmt.Fprintln(w, "  - commit, and the first push from main publishes the APIs")
			fmt.Fprintln(w, "  - guide: "+onboardingGuide)
			if errs > 0 {
				return errFindings
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "the repository root")
	cmd.Flags().StringVar(&opts.Owner, "owner", "", "the owning team's slug (required)")
	cmd.Flags().StringVar(&opts.System, "system", "", "optional grouping shown in the catalogue")
	cmd.Flags().StringVar(&opts.Service, "service", "", "names the APIs <service>-http and <service>-events (default: the directory's name)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "print the descriptor instead of writing it")
	cmd.Flags().StringVar(&ci, "ci", "", "also write the CI workflow: github")
	cmd.Flags().StringVar(&portalURL, "portal-url", "", "the portal's URL, for the CI workflow")
	cmd.Flags().StringVar(&cliVersion, "cli-version", "", "the portal CLI release the workflow installs (default: this binary's)")
	return cmd
}
