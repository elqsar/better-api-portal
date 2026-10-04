package initkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/descriptor"
)

const example = "../../docs/spec/examples"

func TestDetect(t *testing.T) {
	specs, findings, err := Detect("testdata/shop-service")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range specs {
		got = append(got, s.Path+" "+string(s.Kind)+" "+s.Title)
	}
	// node_modules and hidden directories are skipped; JSON Schemas aren't
	// specs.
	want := []string{
		"events/catalog.yaml cloudevents Shop events",
		"specs/admin.yaml openapi Admin API",
		"specs/public/openapi.json openapi Public API",
	}
	if !slices.Equal(got, want) {
		t.Errorf("specs = %q\nwant %q", got, want)
	}
	if len(findings) != 1 || findings[0].File != "legacy/swagger.yaml" || findings[0].RuleID != "spec-unsupported-version" {
		t.Errorf("findings = %+v", findings)
	}
}

func TestPropose(t *testing.T) {
	specs, _, err := Detect("testdata/shop-service")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range Propose(specs, ServiceName("testdata/shop-service")) {
		ids = append(ids, a.ID)
	}
	// One event spec takes the service's name; two OpenAPI specs are named
	// from their titles.
	if want := []string{"shop-events", "admin-http", "public-http"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %q, want %q", ids, want)
	}

	// Collisions and names that aren't valid ids.
	same := []Spec{{Path: "a.yaml", Kind: descriptor.KindOpenAPI, Title: "X"}, {Path: "b.yaml", Kind: descriptor.KindOpenAPI, Title: "X"}}
	ids = nil
	for _, a := range Propose(same, "") {
		ids = append(ids, a.ID)
	}
	if want := []string{"x-http", "x-http-2"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %q, want %q", ids, want)
	}
	for in, want := range map[string]string{"9lives-http": "api-9lives-http", "Ünïcode Svc!": "n-code-svc", "ab": "ab-api"} {
		if got := validID(in); got != want {
			t.Errorf("validID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServiceName(t *testing.T) {
	for in, want := range map[string]string{"/src/orders-service": "orders", "/src/Payments_SVC": "payments", "/src/billing": "billing", "/src/api": "api"} {
		if got := ServiceName(in); got != want {
			t.Errorf("ServiceName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDescriptorExample rebuilds the example's descriptor: the same APIs,
// and a file the descriptor schema accepts.
func TestDescriptorExample(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join(example, "orders-service"))); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "portal.yaml"))
	specs, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "portal.yaml")
	if err := os.WriteFile(p, Descriptor(Options{Owner: "team-orders"}, Propose(specs, "orders")), 0o644); err != nil {
		t.Fatal(err)
	}
	d, findings, err := descriptor.Load(p)
	if err != nil || len(findings) > 0 {
		t.Fatalf("%v %+v", err, findings)
	}
	real, _, err := descriptor.Load(filepath.Join(example, "orders-service/portal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	key := func(d *descriptor.Descriptor) []string {
		var out []string
		for _, a := range d.APIs {
			out = append(out, a.ID+" "+string(a.Kind)+" "+a.Spec)
		}
		slices.Sort(out)
		return out
	}
	if got, want := key(d), key(real); !slices.Equal(got, want) {
		t.Errorf("apis = %q, want %q", got, want)
	}
}

// TestWorkflowExample keeps the documented workflow and the template the
// same.
func TestWorkflowExample(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(example, "ci/github-actions.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := Workflow(examplePortalURL, exampleCLIVersion); string(got) != string(doc) {
		t.Error("templates/github-actions.yml differs from docs/spec/examples/ci/github-actions.yml; change both")
	}
	w := string(Workflow("https://portal.test", "v1.2.3"))
	for _, s := range []string{"PORTAL_URL: https://portal.test\n", "PORTAL_CLI_VERSION: v1.2.3\n"} {
		if !strings.Contains(w, s) {
			t.Errorf("workflow lacks %q", s)
		}
	}
}

func TestAgents(t *testing.T) {
	apis := []API{{ID: "orders-http", Spec: Spec{Path: "api/openapi.yaml"}}}
	section := AgentsSection("https://portal.test/", apis)
	for _, s := range []string{"https://portal.test/mcp", "https://portal.test/tokens", "https://portal.test/llms.txt",
		"- `orders-http`: `api/openapi.yaml`", "portal check --baseline-from https://portal.test"} {
		if !strings.Contains(string(section), s) {
			t.Errorf("section lacks %q:\n%s", s, section)
		}
	}

	// Appended to a file, then replaced in place on a rerun.
	once := MergeAgents([]byte("# Shop\n\nBuild with task.\n"), section)
	if !strings.HasPrefix(string(once), "# Shop\n\nBuild with task.\n\n"+agentsStart) {
		t.Errorf("appended:\n%s", once)
	}
	edited := strings.Replace(string(once), agentsEnd+"\n", agentsEnd+"\n\n## Later\n", 1)
	newer := AgentsSection("https://portal2.test", nil)
	twice := string(MergeAgents([]byte(edited), newer))
	if strings.Count(twice, agentsStart) != 1 || strings.Contains(twice, "portal.test/") ||
		!strings.HasSuffix(twice, agentsEnd+"\n\n## Later\n") || !strings.Contains(twice, "Build with task.") {
		t.Errorf("replaced:\n%s", twice)
	}
	if got := string(MergeAgents(nil, section)); got != string(section) {
		t.Errorf("new file:\n%s", got)
	}

	dir := t.TempDir()
	if got := AgentsFile(dir); got != filepath.Join(dir, "AGENTS.md") {
		t.Errorf("no file: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), nil, 0o644)
	if got := AgentsFile(dir); got != filepath.Join(dir, "CLAUDE.md") {
		t.Errorf("only CLAUDE.md: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), nil, 0o644)
	if got := AgentsFile(dir); got != filepath.Join(dir, "AGENTS.md") {
		t.Errorf("both: %s", got)
	}
}
