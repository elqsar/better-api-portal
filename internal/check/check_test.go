package check

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/config"
	"better-api-portal/internal/model"
)

func TestRunExample(t *testing.T) {
	cfg, err := config.Load("../../docs/spec/examples/portal.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(example+"/portal.yaml", Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	// Recorded from a run with vacuum v0.30.6: no errors; the events API
	// deliberately leaves one produced event undescribed, and the HTTP API
	// gets vacuum's recommended warnings plus two unbounded integers.
	counts := map[string]int{}
	for _, f := range r.Findings {
		if f.Severity != model.SeverityWarn {
			t.Errorf("unexpected %s finding: %+v", f.Severity, f)
		}
		counts[f.API+" "+f.RuleID]++
	}
	want := map[string]int{
		"orders-events ce-description":      1,
		"orders-http sec-integer-bounds":    2,
		"orders-http oas3-missing-example":  8,
		"orders-http component-description": 4,
		"orders-http operation-description": 1,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("findings = %v, want %v", counts, want)
	}
	wantAPIs := []APIResult{{ID: "orders-http", Score: 70, Version: "2.3.0"}, {ID: "orders-events", Score: 98, Version: "1.4.0"}}
	if !reflect.DeepEqual(r.APIs, wantAPIs) {
		t.Errorf("apis = %+v, want %+v", r.APIs, wantAPIs)
	}
}

func TestRunReportsEachProblemOnce(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"portal.yaml": "apiVersion: portal/v1\nowner: team-a\napis:\n" +
			"  - {id: old-events, kind: cloudevents, spec: old.yaml, lifecycle: production}\n" +
			"  - {id: new-events, kind: cloudevents, spec: new.yaml, lifecycle: production}\n",
		// An unsupported version: the descriptor reports it; the parser must not run.
		"old.yaml": "eventcatalog: \"2.0\"\n",
		// Supported, but its dataschema doesn't resolve: only the parser can tell.
		"new.yaml": "eventcatalog: \"1.0\"\ntitle: T\nversion: 1.0.0\nmessages:\n" +
			"  - {type: com.acme.a.b.v1, role: produces, summary: S, description: D, dataschema: {$ref: ./nope.json}, bindings: [{sqs: {queue: q}}]}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Run(filepath.Join(dir, "portal.yaml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, f := range r.Findings {
		rules = append(rules, f.RuleID)
	}
	if len(rules) != 2 || rules[0] != "spec-unsupported-version" || rules[1] != "ce-dataschema-resolves" {
		t.Fatalf("rules = %v, want [spec-unsupported-version ce-dataschema-resolves]", rules)
	}
}

// baselinePair copies the example to base/ and head/ and applies edits to
// head/, as a PR would.
func baselinePair(t *testing.T, edits map[string][2]string) (base, head string) {
	t.Helper()
	root := t.TempDir()
	base, head = filepath.Join(root, "base"), filepath.Join(root, "head")
	for _, dir := range []string{base, head} {
		if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
			t.Fatal(err)
		}
	}
	for name, e := range edits {
		p := filepath.Join(head, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(b), e[0], e[1], 1)
		if s == string(b) {
			t.Fatalf("%s: %q not found", name, e[0])
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(base, "portal.yaml"), filepath.Join(head, "portal.yaml")
}

const example = "../../docs/spec/examples/orders-service"

var dropCustomerID = [2]string{`"required": ["orderId", "customerId", "total"]`, `"required": ["orderId", "total"]`}

func eventsAPI(t *testing.T, r *Report) APIResult {
	t.Helper()
	for _, a := range r.APIs {
		if a.ID == "orders-events" || a.ID == "orders-events-v2" {
			return a
		}
	}
	t.Fatalf("no events API in %+v", r.APIs)
	return APIResult{}
}

// errorsOf lists "rule id" of error findings.
func errorsOf(r *Report) []string {
	var out []string
	for _, f := range r.Findings {
		if f.Severity == model.SeverityError {
			out = append(out, strings.TrimSpace(f.RuleID+" "+f.ID))
		}
	}
	return out
}

func TestRunBaseline(t *testing.T) {
	const schema, events = "api/schemas/order-created.v1.json", "api/events.yaml"

	t.Run("breaking change on a minor bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{schema: dropCustomerID, events: {"version: 1.4.0", "version: 1.5.0"}})
		r, err := Run(head, Options{Baseline: base})
		if err != nil {
			t.Fatal(err)
		}
		errs := errorsOf(r)
		if len(errs) != 1 || !strings.HasPrefix(errs[0], "compat-required BRK-CE-") {
			t.Fatalf("errors = %v", errs)
		}
		a := eventsAPI(t, r)
		if a.BaselineVersion != "1.4.0" || a.Version != "1.5.0" || len(a.Changes) != 1 || a.Score != 98 {
			t.Errorf("api = %+v", a)
		}

		// Acknowledging it by id clears the error.
		id := strings.TrimPrefix(errs[0], "compat-required ")
		r, err = Run(head, Options{Baseline: base, Acks: map[string]string{id: "customerId is always set"}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors after ack = %v", errs)
		}
	})

	t.Run("breaking change on a major bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{schema: dropCustomerID, events: {"version: 1.4.0", "version: 2.0.0"}})
		r, err := Run(head, Options{Baseline: base})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors = %v", errs)
		}
	})

	t.Run("content changed without a version bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{schema: dropCustomerID})
		r, err := Run(head, Options{Baseline: base})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); !slices.Equal(errs, []string{"semver-unchanged"}) {
			t.Errorf("errors = %v", errs)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		base, head := baselinePair(t, nil)
		r, err := Run(head, Options{Baseline: base})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 || len(eventsAPI(t, r).Changes) > 0 {
			t.Errorf("errors = %v, changes = %+v", errs, eventsAPI(t, r).Changes)
		}
		// OpenAPI isn't diffed yet, and says so.
		var noted bool
		for _, f := range r.Findings {
			noted = noted || (f.API == "orders-http" && f.RuleID == "diff-unsupported" && f.Severity == model.SeverityInfo)
		}
		if !noted {
			t.Error("no diff-unsupported note for orders-http")
		}
	})

	t.Run("API not in the baseline is a first version", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{"portal.yaml": {"id: orders-events", "id: orders-events-v2"}})
		r, err := Run(head, Options{Baseline: base})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 || eventsAPI(t, r).BaselineVersion != "" {
			t.Errorf("errors = %v, api = %+v", errs, eventsAPI(t, r))
		}
	})

	t.Run("broken baseline is an error, not a finding", func(t *testing.T) {
		base, head := baselinePair(t, nil)
		if err := os.WriteFile(filepath.Join(filepath.Dir(base), events), []byte("eventcatalog: \"1.0\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(head, Options{Baseline: base}); err == nil || !strings.Contains(err.Error(), "baseline") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRunKindChanged(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"base/portal.yaml":  "apiVersion: portal/v1\nowner: team-a\napis:\n  - {id: things, kind: openapi, spec: openapi.yaml, lifecycle: production}\n",
		"base/openapi.yaml": "openapi: 3.1.0\ninfo: {title: T, version: 1.0.0}\npaths: {}\n",
		"head/portal.yaml":  "apiVersion: portal/v1\nowner: team-a\napis:\n  - {id: things, kind: cloudevents, spec: events.yaml, lifecycle: production}\n",
		"head/events.yaml": "eventcatalog: \"1.0\"\ntitle: T\nversion: 1.0.0\nmessages:\n" +
			"  - {type: com.acme.a.b.v1, role: produces, summary: S, description: D, dataschema: {schema: {}}, bindings: [{sqs: {queue: q}}]}\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Run(filepath.Join(dir, "head/portal.yaml"), Options{Baseline: filepath.Join(dir, "base/portal.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if errs := errorsOf(r); !slices.Equal(errs, []string{"api-kind-changed"}) {
		t.Errorf("errors = %v", errs)
	}
}

func TestDiffFiles(t *testing.T) {
	base, head := baselinePair(t, map[string][2]string{"api/schemas/order-created.v1.json": dropCustomerID})
	oldP := filepath.Join(filepath.Dir(base), "api/events.yaml")
	newP := filepath.Join(filepath.Dir(head), "api/events.yaml")
	changes, fs, err := DiffFiles(oldP, newP, "")
	if err != nil || len(fs) > 0 {
		t.Fatalf("err = %v, findings = %+v", err, fs)
	}
	if len(changes) != 1 || changes[0].RuleID != "compat-required" {
		t.Errorf("changes = %+v", changes)
	}
	if _, _, err := DiffFiles(filepath.Join(example, "api/openapi.yaml"), newP, ""); err == nil ||
		!strings.Contains(err.Error(), "not supported yet") {
		t.Errorf("openapi diff err = %v", err)
	}
}
