package check

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"better-api-portal/internal/config"
	"better-api-portal/internal/model"
)

func TestRunExample(t *testing.T) {
	cfg, err := config.Load("../../docs/spec/examples/portal.config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run("../../docs/spec/examples/orders-service/portal.yaml", Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	// The example deliberately leaves one produced event undescribed.
	if len(r.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(r.Findings), r.Findings)
	}
	f := r.Findings[0]
	if f.RuleID != "ce-description" || f.Severity != model.SeverityWarn || f.API != "orders-events" ||
		f.Pointer != "/messages/1" || f.Line != 32 {
		t.Errorf("finding = %+v", f)
	}
	want := []APIResult{{ID: "orders-events", Score: 98}}
	if !slices.Equal(r.APIs, want) {
		t.Errorf("apis = %+v, want %+v", r.APIs, want)
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
