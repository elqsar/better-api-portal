package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/client"
	"github.com/elqsar/better-api-portal/internal/config"
	"github.com/elqsar/better-api-portal/internal/descriptor"
	"github.com/elqsar/better-api-portal/internal/httpapi"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/report"
)

// reportFlags are --format and --output, shared by check and push.
type reportFlags struct {
	format  string
	outputs []string
	strict  bool
}

func (f *reportFlags) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.format, "format", "text", "stdout format: text, json, sarif or junit")
	cmd.Flags().StringArrayVar(&f.outputs, "output", nil, "also write format=path, e.g. sarif=portal.sarif (repeatable)")
}

// reportOutputs renders a report to stdout and to the --output files.
type reportOutputs struct {
	renderers map[string]func(io.Writer, *check.Report) error
	format    string
	files     [][2]string // format, path
}

// prepare validates the flags before anything runs.
func (f *reportFlags) prepare() (*reportOutputs, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	o := &reportOutputs{format: f.format, renderers: map[string]func(io.Writer, *check.Report) error{
		"text":  report.Text,
		"json":  report.JSON,
		"sarif": func(w io.Writer, r *check.Report) error { return report.SARIF(w, r, root) },
		"junit": func(w io.Writer, r *check.Report) error { return report.JUnit(w, r, f.strict) },
	}}
	if _, ok := o.renderers[f.format]; !ok {
		return nil, fmt.Errorf("unknown --format %q (want text, json, sarif or junit)", f.format)
	}
	seen := map[string]bool{}
	for _, out := range f.outputs {
		format, p, _ := strings.Cut(out, "=")
		if _, ok := o.renderers[format]; !ok || p == "" {
			return nil, fmt.Errorf("--output %q: want format=path, with format text, json, sarif or junit", out)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		if seen[abs] {
			return nil, fmt.Errorf("--output %q: %s is already an output", out, p)
		}
		seen[abs] = true
		o.files = append(o.files, [2]string{format, p})
	}
	return o, nil
}

// write renders r to stdout, then to each file.
func (o *reportOutputs) write(stdout io.Writer, r *check.Report) error {
	if err := o.renderers[o.format](stdout, r); err != nil {
		return err
	}
	for _, f := range o.files {
		var buf bytes.Buffer
		if err := o.renderers[f[0]](&buf, r); err != nil {
			return err
		}
		if err := os.WriteFile(f[1], buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func ackMap(acks []string, reason string) (map[string]string, error) {
	if len(acks) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("--ack needs --ack-reason: say why the breaking change is safe")
	}
	m := map[string]string{}
	for _, id := range acks {
		m[id] = reason
	}
	return m, nil
}

// fetchBaselines gets each API's latest published version from the portal,
// logging what it found to log.
func fetchBaselines(ctx context.Context, log io.Writer, descPath, portalURL, audience string) (map[string]check.Baseline, error) {
	d, _, err := descriptor.Load(descPath)
	if err != nil || d == nil {
		return nil, err // check reports the descriptor's problems
	}
	c, err := client.New(portalURL, client.FromEnv(os.Getenv, audience))
	if err != nil {
		return nil, err
	}
	out := map[string]check.Baseline{}
	for _, api := range d.APIs {
		if api.Kind == descriptor.KindAsyncAPI {
			continue // not diffed yet
		}
		b, version, err := c.Latest(ctx, api.ID)
		if err != nil {
			return nil, fmt.Errorf("baseline of %s: %w", api.ID, err)
		}
		if b == nil {
			fmt.Fprintf(log, "%s: no published version on the portal; not diffed\n", api.ID)
			continue
		}
		fmt.Fprintf(log, "%s: baseline %s from the portal\n", api.ID, version)
		out[api.ID] = *b
	}
	return out, nil
}

func pushCmd() *cobra.Command {
	var (
		descPath  string
		portalURL string
		audience  string
		acks      []string
		ackReason string
		dryRun    bool
		reports   reportFlags
	)
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Publish the descriptor's APIs to the portal",
		Long: `Bundle every API in the descriptor and push them to the portal, which checks
them against its latest published versions and publishes those that pass.
Exits 1 if any API is rejected. A retry of the same push is a no-op
("unchanged"), so network errors and server errors are retried.

The portal is --url or $PORTAL_URL. Credentials come from the environment:
  PORTAL_TOKEN   a token from portal admin token create, or an ID token
                 (in GitLab CI: id_tokens: {PORTAL_TOKEN: {aud: api-portal}})
  GitHub Actions an ID token is requested for --audience; the job needs
                 permissions: id-token: write
--dry-run asks the portal's /check instead: the same verdict, nothing stored.

--format and --output work as for check; json includes each API's status and
URL.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := reports.prepare()
			if err != nil {
				return err
			}
			ackReasons, err := ackMap(acks, ackReason)
			if err != nil {
				return err
			}
			if portalURL == "" {
				portalURL = os.Getenv("PORTAL_URL")
			}
			if portalURL == "" {
				return errors.New("no portal: pass --url or set PORTAL_URL")
			}
			c, err := client.New(portalURL, client.FromEnv(os.Getenv, audience))
			if err != nil {
				return err
			}

			d, findings, err := descriptor.Load(descPath)
			if err != nil {
				return err
			}
			if d == nil {
				return localFailure(cmd.OutOrStdout(), descPath, findings)
			}
			packed, fs, err := check.BundleAPIs(d)
			if err != nil {
				return err
			}
			findings = append(findings, fs...)
			if model.Counts(findings)[model.SeverityError] > 0 {
				return localFailure(cmd.OutOrStdout(), descPath, findings)
			}
			desc, err := os.ReadFile(descPath)
			if err != nil {
				return err
			}

			resp, err := c.Push(cmd.Context(), desc, packed, ackReasons, dryRun)
			if err != nil {
				return err
			}
			r := pushReport(descPath, d.Dir, resp)
			// pushReport rewrote the paths in resp too: they share slices.
			out.renderers["json"] = func(w io.Writer, _ *check.Report) error {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(resp)
			}
			out.renderers["text"] = func(w io.Writer, r *check.Report) error {
				if err := report.Text(w, r); err != nil {
					return err
				}
				for _, a := range resp.APIs {
					line := fmt.Sprintf("%s %s: %s", a.ID, a.Version, a.Status)
					if a.URL != "" {
						line += "  " + a.URL
					}
					if _, err := fmt.Fprintln(w, line); err != nil {
						return err
					}
				}
				return nil
			}
			if err := out.write(cmd.OutOrStdout(), r); err != nil {
				return err
			}
			for _, a := range resp.APIs {
				if a.Status == httpapi.StatusRejected {
					return errFindings
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&descPath, "descriptor", "portal.yaml", "path to the descriptor")
	cmd.Flags().StringVar(&portalURL, "url", "", "the portal's URL (default $PORTAL_URL)")
	cmd.Flags().StringVar(&audience, "audience", config.DefaultAudience, "audience of the GitHub Actions ID token")
	cmd.Flags().StringArrayVar(&acks, "ack", nil, "acknowledge a breaking change by id (repeatable)")
	cmd.Flags().StringVar(&ackReason, "ack-reason", "", "why the acknowledged changes are safe (required with --ack)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "have the portal check the push without storing it")
	reports.flags(cmd)
	return cmd
}

// localFailure prints problems found before anything was sent.
func localFailure(w io.Writer, descPath string, findings []model.Finding) error {
	if err := report.Text(w, &check.Report{Descriptor: descPath, Findings: findings}); err != nil {
		return err
	}
	return errFindings
}

// pushReport turns a push response into a report whose paths, which the
// portal gives relative to the descriptor, are relative to the working
// directory again, as check's are.
func pushReport(descPath, dir string, resp *httpapi.PushResponse) *check.Report {
	local := func(p string) string {
		if p == "" || strings.HasPrefix(p, "baseline:") {
			return p
		}
		return filepath.Join(dir, filepath.FromSlash(p))
	}
	r := &check.Report{Descriptor: descPath, Findings: resp.Findings}
	for i := range r.Findings {
		r.Findings[i].File = local(r.Findings[i].File)
	}
	for _, a := range resp.APIs {
		res := a.APIResult
		for j := range res.Changes {
			res.Changes[j].File = local(res.Changes[j].File)
		}
		r.APIs = append(r.APIs, res)
	}
	return r
}
