package check

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
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
	// gets one warning from vacuum's trimmed recommended ruleset plus two
	// unbounded integers.
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
		"orders-http operation-description": 1,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("findings = %v, want %v", counts, want)
	}
	// The hashes are pinned: a change to the canonical form would make the
	// server see every existing version as changed.
	wantAPIs := []APIResult{
		{ID: "orders-http", Score: 94, Version: "2.3.0", ContentHash: "sha256:9a0ece62bdea04cc8070282535111ec69d89c8e297355cdc8dd506911fc39bc3"},
		{ID: "orders-events", Score: 98, Version: "1.4.0", ContentHash: "sha256:7bb0bceacabdd16ba201c1a0cad55c6b08fc409eb099233d5dcc0be731d57a21"},
	}
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
		r, err := Run(head, Options{Baselines: []string{base}})
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
		r, err = Run(head, Options{Baselines: []string{base}, Acks: map[string]string{id: "customerId is always set"}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors after ack = %v", errs)
		}
	})

	t.Run("breaking change on a major bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{schema: dropCustomerID, events: {"version: 1.4.0", "version: 2.0.0"}})
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors = %v", errs)
		}
	})

	t.Run("content changed without a version bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{schema: dropCustomerID})
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); !slices.Equal(errs, []string{"semver-unchanged"}) {
			t.Errorf("errors = %v", errs)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		base, head := baselinePair(t, nil)
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 || len(eventsAPI(t, r).Changes) > 0 {
			t.Errorf("errors = %v, changes = %+v", errs, eventsAPI(t, r).Changes)
		}
		if a := httpAPI(t, r); a.BaselineVersion != "2.3.0" || len(a.Changes) > 0 {
			t.Errorf("http api = %+v", a)
		}
	})

	t.Run("API not in the baseline is a first version", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{"portal.yaml": {"id: orders-events", "id: orders-events-v2"}})
		r, err := Run(head, Options{Baselines: []string{base}})
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
		if _, err := Run(head, Options{Baselines: []string{base}}); err == nil || !strings.Contains(err.Error(), "baseline") {
			t.Errorf("err = %v", err)
		}
	})
}

var dropOutOfStock = [2]string{"enum: [customer_request, payment_failed, out_of_stock]", "enum: [customer_request, payment_failed]"}

func TestRunBaselineOpenAPI(t *testing.T) {
	const spec = "api/openapi.yaml"

	t.Run("breaking change on a minor bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{spec: dropOutOfStock})
		// Two edits to one file: apply the bump on top.
		bump(t, head, spec, "version: 2.3.0", "version: 2.4.0")
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		errs := errorsOf(r)
		if len(errs) != 1 || !strings.HasPrefix(errs[0], "request-property-enum-value-removed BRK-OA-") {
			t.Fatalf("errors = %v", errs)
		}
		a := httpAPI(t, r)
		if a.BaselineVersion != "2.3.0" || a.Version != "2.4.0" || len(a.Changes) != 1 {
			t.Errorf("api = %+v", a)
		}

		id := strings.TrimPrefix(errs[0], "request-property-enum-value-removed ")
		r, err = Run(head, Options{Baselines: []string{base}, Acks: map[string]string{id: "nobody sends out_of_stock"}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors after ack = %v", errs)
		}
	})

	t.Run("breaking change on a major bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{spec: dropOutOfStock})
		bump(t, head, spec, "version: 2.3.0", "version: 3.0.0")
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors = %v", errs)
		}
	})

	t.Run("content changed without a version bump", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{spec: {"summary: Get an order", "summary: Fetch an order"}})
		r, err := Run(head, Options{Baselines: []string{base}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); !slices.Equal(errs, []string{"semver-unchanged"}) {
			t.Errorf("errors = %v", errs)
		}
	})
}

// bump applies a further edit to a file in head's directory.
func bump(t *testing.T, head, name, old, new string) {
	t.Helper()
	p := filepath.Join(filepath.Dir(head), name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), old, new, 1)
	if s == string(b) {
		t.Fatalf("%s: %q not found", name, old)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func httpAPI(t *testing.T, r *Report) APIResult {
	t.Helper()
	for _, a := range r.APIs {
		if a.ID == "orders-http" {
			return a
		}
	}
	t.Fatalf("no HTTP API in %+v", r.APIs)
	return APIResult{}
}

// packBase bundles the API id of the descriptor at base into dir and
// returns the "id=file" baseline argument.
func packBase(t *testing.T, base, id string) string {
	t.Helper()
	d, _, err := descriptor.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	for i, a := range d.APIs {
		if a.ID != id {
			continue
		}
		c, problems, err := bundle.Load(d.Dir, d.SpecPath(i))
		if err != nil || len(problems) > 0 {
			t.Fatalf("closure: %v %+v", err, problems)
		}
		b, err := bundle.Read(c.Root, c.Files())
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), id+".tar.zst")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := b.Pack(f); err != nil {
			t.Fatal(err)
		}
		return id + "=" + p
	}
	t.Fatalf("no API %s in %s", id, base)
	return ""
}

func TestRunBundleBaselines(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{
			"api/schemas/order-created.v1.json": dropCustomerID, "api/events.yaml": {"version: 1.4.0", "version: 1.5.0"}})
		r, err := Run(head, Options{Baselines: []string{packBase(t, base, "orders-events")}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) != 1 || !strings.HasPrefix(errs[0], "compat-required BRK-CE-") {
			t.Errorf("errors = %v", errs)
		}
		if a := httpAPI(t, r); a.BaselineVersion != "" {
			t.Errorf("orders-http has no baseline, yet = %+v", a)
		}
	})

	t.Run("openapi, with the descriptor for the rest", func(t *testing.T) {
		base, head := baselinePair(t, map[string][2]string{"api/openapi.yaml": dropOutOfStock})
		bump(t, head, "api/openapi.yaml", "version: 2.3.0", "version: 2.4.0")
		r, err := Run(head, Options{Baselines: []string{base, packBase(t, base, "orders-http")}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) != 1 || !strings.HasPrefix(errs[0], "request-property-enum-value-removed BRK-OA-") {
			t.Errorf("errors = %v", errs)
		}
		if a := eventsAPI(t, r); a.BaselineVersion != "1.4.0" {
			t.Errorf("orders-events wasn't compared with the descriptor baseline: %+v", a)
		}
	})

	t.Run("unchanged", func(t *testing.T) {
		base, head := baselinePair(t, nil)
		r, err := Run(head, Options{Baselines: []string{packBase(t, base, "orders-http"), packBase(t, base, "orders-events")}})
		if err != nil {
			t.Fatal(err)
		}
		if errs := errorsOf(r); len(errs) > 0 {
			t.Errorf("errors = %v", errs)
		}
	})

	for name, args := range map[string]func(base string) []string{
		"unknown id": func(base string) []string {
			return []string{"nope=" + strings.TrimPrefix(packBase(t, base, "orders-http"), "orders-http=")}
		},
		"duplicate id":        func(base string) []string { a := packBase(t, base, "orders-http"); return []string{a, a} },
		"two descriptors":     func(base string) []string { return []string{base, base} },
		"not a bundle":        func(base string) []string { return []string{"orders-http=" + base} },
		"missing bundle file": func(base string) []string { return []string{"orders-http=" + base + ".tar.zst"} },
	} {
		t.Run(name, func(t *testing.T) {
			base, head := baselinePair(t, nil)
			if _, err := Run(head, Options{Baselines: args(base)}); err == nil || !strings.Contains(err.Error(), "baseline") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestRunFormattingOnlyChange(t *testing.T) {
	// Same content hash: not a change that needs a version bump.
	base, head := baselinePair(t, map[string][2]string{"api/openapi.yaml": {"openapi: 3.1.0", "# reformatted\nopenapi:    3.1.0"}})
	r, err := Run(head, Options{Baselines: []string{base}})
	if err != nil {
		t.Fatal(err)
	}
	if errs := errorsOf(r); len(errs) > 0 {
		t.Errorf("errors = %v", errs)
	}
	if a := httpAPI(t, r); a.ContentHash == "" {
		t.Errorf("no content hash: %+v", a)
	}
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
	r, err := Run(filepath.Join(dir, "head/portal.yaml"), Options{Baselines: []string{filepath.Join(dir, "base/portal.yaml")}})
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
		!strings.Contains(err.Error(), "same kind") {
		t.Errorf("mixed-kind diff err = %v", err)
	}

	base, head = baselinePair(t, map[string][2]string{"api/openapi.yaml": dropOutOfStock})
	changes, fs, err = DiffFiles(filepath.Join(filepath.Dir(base), "api/openapi.yaml"),
		filepath.Join(filepath.Dir(head), "api/openapi.yaml"), "")
	if err != nil || len(fs) > 0 {
		t.Fatalf("err = %v, findings = %+v", err, fs)
	}
	if len(changes) != 1 || changes[0].Impact != model.ImpactBreaking {
		t.Errorf("openapi changes = %+v", changes)
	}
}
