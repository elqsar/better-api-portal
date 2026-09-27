// Package initkit scaffolds a service repo for the portal (portal init):
// it finds the repo's specs, proposes a portal.yaml for them, and renders
// the CI workflow.
package initkit

import (
	"bytes"
	"cmp"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/elqsar/better-api-portal/internal/descriptor"
	"github.com/elqsar/better-api-portal/internal/model"
)

// Spec is a spec file found in the repo.
type Spec struct {
	Path  string // relative to the root, with forward slashes
	Kind  descriptor.Kind
	Title string // info.title or title, if any
}

// skipDirs are never searched: dependencies, build output, fixtures.
var skipDirs = []string{"node_modules", "vendor", "dist", "bin", "build", "target", "testdata"}

// maxSpecSize bounds the files read while searching.
const maxSpecSize = 8 << 20

// Detect finds the specs under root: every YAML or JSON file that
// descriptor.Sniff recognises. Hidden directories (.git, .github, .portal)
// and skipDirs are left out. Files in a format the portal doesn't support
// (Swagger 2, AsyncAPI 2) come back as findings.
func Detect(root string) ([]Spec, []model.Finding, error) {
	var specs []Spec
	var findings []model.Finding
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := e.Name()
		if e.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || slices.Contains(skipDirs, name)) {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".yaml", ".yml", ".json":
		default:
			return nil
		}
		if info, err := e.Info(); err != nil || info.Size() > maxSpecSize || !e.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		kind, f, err := descriptor.Sniff(p)
		if err != nil {
			return err
		}
		if f != nil && f.RuleID == "spec-unsupported-version" {
			f.File = rel
			findings = append(findings, *f)
		}
		if kind != "" {
			specs = append(specs, Spec{Path: rel, Kind: kind, Title: title(p)})
		}
		return nil
	})
	return specs, findings, err
}

// title reads info.title (OpenAPI, AsyncAPI) or title (event catalogue).
func title(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Title string `yaml:"title"`
		Info  struct {
			Title string `yaml:"title"`
		} `yaml:"info"`
	}
	if yaml.Unmarshal(b, &doc) != nil {
		return ""
	}
	return strings.TrimSpace(cmp.Or(doc.Info.Title, doc.Title))
}

// Options are what portal init asks for.
type Options struct {
	Owner  string // team slug
	System string // optional
	// Service names the APIs: <service>-http, <service>-events. Defaults to
	// the root directory's name without a -service, -svc or -api suffix.
	Service string
}

// API is a proposed apis[] entry.
type API struct {
	ID, Title string
	Spec      Spec
}

var (
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
	apiID   = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
)

// Slug turns a name into a lower-case, dash-separated id fragment.
func Slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// ServiceName is the default Options.Service for a repo root.
func ServiceName(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	s := Slug(filepath.Base(abs))
	for _, suffix := range []string{"-service", "-svc", "-api"} {
		if t := strings.TrimSuffix(s, suffix); t != "" && t != s {
			return t
		}
	}
	return s
}

func kindSuffix(k descriptor.Kind) string {
	if k == descriptor.KindOpenAPI {
		return "http"
	}
	return "events"
}

// Propose names an API per spec: <service>-http for the only OpenAPI spec,
// <service>-events for the only event spec; when a kind has several, each
// is named from its title (or file name) instead. Ids are made valid and
// unique.
func Propose(specs []Spec, service string) []API {
	perKind := map[string]int{}
	for _, s := range specs {
		perKind[kindSuffix(s.Kind)]++
	}
	var out []API
	taken := map[string]bool{}
	for _, s := range specs {
		suffix := kindSuffix(s.Kind)
		base := service
		if perKind[suffix] > 1 || base == "" {
			name := s.Title
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(s.Path), filepath.Ext(s.Path))
			}
			base = Slug(name)
			base = strings.TrimSuffix(strings.TrimSuffix(base, "-"+suffix), "-api")
		}
		id := validID(base + "-" + suffix)
		for n := 2; taken[id]; n++ {
			id = validID(fmt.Sprintf("%s-%s-%d", base, suffix, n))
		}
		taken[id] = true
		out = append(out, API{ID: id, Title: s.Title, Spec: s})
	}
	return out
}

// validID fits a candidate to ^[a-z][a-z0-9-]{2,62}$.
func validID(id string) string {
	id = Slug(id)
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = "api-" + id
	}
	if len(id) > 63 {
		id = strings.TrimRight(id[:63], "-")
	}
	if !apiID.MatchString(id) {
		id = strings.TrimRight(id+"-api", "-")
	}
	return id
}

// Descriptor renders portal.yaml for the APIs, with comments that say what
// each field is for.
func Descriptor(opts Options, apis []API) []byte {
	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Which APIs this repo publishes to the API portal, and who owns them.\n")
	w("# Format: https://github.com/elqsar/better-api-portal/blob/main/docs/spec/03-formats.md\n")
	w("apiVersion: portal/v1\n")
	w("owner: %s # a team slug from the portal's configuration\n", opts.Owner)
	if opts.System != "" {
		w("system: %s\n", opts.System)
	} else {
		w("# system: commerce # optional grouping shown in the catalogue\n")
	}
	w("# links:\n#   - { title: Runbook, url: \"https://wiki.internal/…\" }\n")
	w("\napis:\n")
	for i, a := range apis {
		if i > 0 {
			w("\n")
		}
		w("  - id: %s # stable forever: it is the API's URL and diff key\n", a.ID)
		w("    kind: %s\n", a.Spec.Kind)
		w("    spec: %s\n", yamlString(a.Spec.Path))
		w("    lifecycle: production # or experimental, deprecated, retired\n")
		if a.Spec.Kind == descriptor.KindOpenAPI {
			w("    # environments:\n    #   - { name: prod, url: \"https://…\" }\n")
		} else {
			w("    # environments: # brokers are named in the portal's configuration\n    #   - { name: prod, broker: kafka-prod }\n")
		}
	}
	w("\n# consumes: # the APIs this repo depends on, so their owners can see it\n")
	w("#   - api: payments-events\n#     types: [com.acme.payments.payment.captured.v1]\n")
	return b.Bytes()
}

// yamlString quotes s if YAML would read it as something else.
func yamlString(s string) string {
	b, _ := yaml.Marshal(s)
	return strings.TrimSuffix(string(b), "\n")
}

//go:embed templates/github-actions.yml
var githubWorkflow string

// Example values in the workflow template, replaced by Workflow.
const (
	examplePortalURL  = "https://api-portal.acme.internal"
	exampleCLIVersion = "v0.1.0"
)

// WorkflowPath is where Workflow's output goes in a GitHub repo.
const WorkflowPath = ".github/workflows/api-portal.yml"

// Workflow renders the GitHub Actions workflow for a portal and a CLI
// release.
func Workflow(portalURL, cliVersion string) []byte {
	s := strings.Replace(githubWorkflow, "PORTAL_URL: "+examplePortalURL, "PORTAL_URL: "+portalURL, 1)
	s = strings.Replace(s, "PORTAL_CLI_VERSION: "+exampleCLIVersion, "PORTAL_CLI_VERSION: "+cliVersion, 1)
	return []byte(s)
}
