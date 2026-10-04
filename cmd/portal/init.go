package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/descriptor"
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
		eventsFrom string
		eventsOut  string
		events     initkit.EventsOptions
		agents     bool
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

--events-from DIR drafts an event catalogue from existing CloudEvents
payload schemas: one produced message per JSON Schema in DIR that no other
schema there refers to. A schema's $id that is already a type is kept;
otherwise the type is --type-prefix plus the file name (order-created.v1.json
→ <prefix>order.created.v1). The catalogue goes next to DIR (--events-out
overrides it) and is added to portal.yaml. --kafka-topic or --nats-subject
sets its default binding. It lists the types that break the portal's naming
rules.

--agents adds a section to the repo's AGENTS.md (CLAUDE.md if only that
exists) telling coding agents to look contracts up in the portal, and how:
its MCP tools, or its Markdown pages. It needs --portal-url. A rerun
replaces the section and leaves the rest of the file alone.

Nothing else is overwritten without --force; --stdout prints the descriptor
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
			if agents && portalURL == "" {
				return errors.New("--agents needs --portal-url, the portal agents look contracts up in")
			}
			if ci != "" && cliVersion == "" {
				v := portalVersion()
				if !semver.IsValid(v) || semver.Prerelease(v) != "" || semver.Build(v) != "" {
					return fmt.Errorf("this portal binary (%s) isn't a release, so --cli-version is required with --ci", v)
				}
				cliVersion = v
			}
			w, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()

			if opts.Service == "" {
				opts.Service = initkit.ServiceName(root)
			}
			specs, unsupported, err := initkit.Detect(root)
			if err != nil {
				return err
			}
			for _, f := range unsupported {
				fmt.Fprintf(errw, "skipped %s: %s\n", f.File, f.Message)
			}

			var draft []byte
			if eventsFrom != "" {
				if eventsOut == "" {
					eventsOut = filepath.Join(filepath.Dir(filepath.Clean(eventsFrom)), "events.yaml")
				}
				rel, err := filepath.Rel(root, eventsOut)
				if err != nil || !filepath.IsLocal(rel) {
					return fmt.Errorf("the event catalogue %s must be inside the repository root %s", eventsOut, root)
				}
				if events.Title == "" {
					events.Title = strings.ToUpper(opts.Service[:1]) + opts.Service[1:] + " events"
				}
				var types []initkit.EventType
				if draft, types, err = initkit.Events(eventsFrom, eventsOut, events); err != nil {
					return err
				}
				printTypes(errw, types, events.Prefix)
				rel = filepath.ToSlash(rel)
				specs = slices.DeleteFunc(specs, func(s initkit.Spec) bool { return s.Path == rel })
				specs = append(specs, initkit.Spec{Path: rel, Kind: descriptor.KindCloudEvents, Title: events.Title})
			}
			if len(specs) == 0 {
				return fmt.Errorf("no OpenAPI 3, AsyncAPI 3 or event catalogue file found under %s; for existing CloudEvents with JSON Schemas, use --events-from (see %s)", root, onboardingGuide)
			}
			proposed := initkit.Propose(specs, opts.Service)
			desc := initkit.Descriptor(opts, proposed)
			var section []byte
			if agents {
				section = initkit.AgentsSection(portalURL, proposed)
			}
			if stdout {
				if draft != nil {
					fmt.Fprintf(w, "# %s\n%s\n# portal.yaml\n", eventsOut, draft)
				}
				if _, err := w.Write(desc); err != nil {
					return err
				}
				if section != nil {
					fmt.Fprintf(w, "\n# %s\n%s", filepath.Base(initkit.AgentsFile(root)), section)
				}
				return nil
			}

			descPath := filepath.Join(root, "portal.yaml")
			files := map[string][]byte{descPath: desc}
			order := []string{descPath}
			if draft != nil {
				files[eventsOut] = draft
				order = append([]string{eventsOut}, order...)
			}
			if ci == "github" {
				p := filepath.Join(root, initkit.WorkflowPath)
				files[p] = initkit.Workflow(portalURL, cliVersion)
				order = append(order, p)
			}
			for p := range files {
				if _, err := os.Stat(p); err == nil && !force {
					return fmt.Errorf("%s exists; pass --force to overwrite it", p)
				} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			for _, p := range order {
				b := files[p]
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(p, b, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(w, "wrote %s\n", p)
			}
			if section != nil {
				p := initkit.AgentsFile(root)
				old, err := os.ReadFile(p)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if err := os.WriteFile(p, initkit.MergeAgents(old, section), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(w, "added the API portal section to %s\n", p)
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
			if !agents {
				fmt.Fprintln(w, "  - tell coding agents about the portal: portal init --agents --portal-url URL --force")
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
	cmd.Flags().StringVar(&eventsFrom, "events-from", "", "draft an event catalogue from the JSON Schemas in this directory")
	cmd.Flags().StringVar(&eventsOut, "events-out", "", "where the drafted catalogue goes (default: events.yaml next to --events-from)")
	cmd.Flags().StringVar(&events.Prefix, "type-prefix", "", "event type prefix for types named from files, e.g. com.acme.orders.")
	cmd.Flags().StringVar(&events.Title, "events-title", "", "the drafted catalogue's title (default: \"<service> events\")")
	cmd.Flags().StringVar(&events.KafkaTopic, "kafka-topic", "", "the drafted catalogue's default Kafka topic")
	cmd.Flags().StringVar(&events.NATSSubject, "nats-subject", "", "the drafted catalogue's default NATS subject")
	cmd.Flags().BoolVar(&agents, "agents", false, "also add an API portal section to AGENTS.md (or CLAUDE.md) for coding agents")
	cmd.Flags().StringVar(&cliVersion, "cli-version", "", "the portal CLI release the workflow installs (default: this binary's)")
	return cmd
}

// printTypes is the inventory of drafted event types (Q6): where each name
// came from and which break the portal's rules.
func printTypes(w io.Writer, types []initkit.EventType, prefix string) {
	fmt.Fprintln(w, "Event types:")
	bad := 0
	for _, t := range types {
		from := "named from " + t.Schema
		if t.FromID {
			from = "$id of " + t.Schema
		}
		fmt.Fprintf(w, "  %s (%s)\n", t.Type, from)
		for _, p := range t.Problems {
			fmt.Fprintf(w, "    ✗ %s\n", p)
			bad++
		}
	}
	if bad > 0 {
		fmt.Fprintln(w, "Types that break the rules fail portal check. A type producers already send can't just be renamed:")
		fmt.Fprintln(w, "publish the new name as a new type alongside it, move consumers over, then deprecate the old one.")
	}
	if prefix == "" {
		fmt.Fprintln(w, "No --type-prefix: types named from files have no organisation prefix.")
	}
}
