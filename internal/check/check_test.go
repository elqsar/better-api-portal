package check

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunExample(t *testing.T) {
	findings, err := Run("../../docs/spec/examples/orders-service/portal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("unexpected findings: %+v", findings)
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
			"  - {type: com.acme.a.b.v1, role: produces, summary: S, dataschema: {$ref: ./nope.json}, bindings: [{sqs: {queue: q}}]}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := Run(filepath.Join(dir, "portal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, f := range findings {
		rules = append(rules, f.RuleID)
	}
	if len(rules) != 2 || rules[0] != "spec-unsupported-version" || rules[1] != "ce-dataschema-resolves" {
		t.Fatalf("rules = %v, want [spec-unsupported-version ce-dataschema-resolves]", rules)
	}
}
