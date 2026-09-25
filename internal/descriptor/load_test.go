package descriptor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"better-api-portal/internal/model"
)

func TestLoadExample(t *testing.T) {
	d, findings, err := Load("../../docs/spec/examples/orders-service/portal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if len(d.APIs) != 2 || d.APIs[1].Kind != KindCloudEvents || d.APIs[1].EffectiveOwner(d) != "team-orders" {
		t.Fatalf("unexpected descriptor: %+v", d)
	}
}

// valid spec files available to every case.
var specs = map[string]string{
	"openapi.yaml":   "openapi: 3.1.0\ninfo: {title: T, version: 1.0.0}\npaths: {}\n",
	"events.yaml":    "eventcatalog: \"1.0\"\ntitle: T\nversion: 1.0.0\nmessages: []\n",
	"asyncapi.yaml":  "asyncapi: 3.0.0\ninfo: {title: T, version: 1.0.0}\n",
	"swagger.yaml":   "swagger: \"2.0\"\ninfo: {title: T, version: 1.0.0}\n",
	"asyncapi2.yaml": "\n\nasyncapi: 2.6.0\n",
	"plain.yaml":     "hello: world\n",
}

// head is the first three lines of every descriptor below, so apis[0] starts on line 4.
const head = "apiVersion: portal/v1\nowner: team-orders\napis:\n"

func TestLoadFindings(t *testing.T) {
	type want struct {
		rule    string
		file    string // relative to the fixture dir; "" means portal.yaml
		line    int
		message string // substring
	}
	tests := []struct {
		name       string
		descriptor string
		want       []want
	}{
		{
			name:       "yaml syntax error",
			descriptor: head + "  - id: [unclosed\n",
			want:       []want{{rule: "descriptor-syntax", line: 3}}, // yaml reports where the flow sequence's context starts
		},
		{
			name:       "wrong apiVersion",
			descriptor: "apiVersion: portal/v2\nowner: team-orders\napis:\n  - {id: orders-http, kind: openapi, spec: openapi.yaml, lifecycle: production}\n",
			want:       []want{{rule: "descriptor-schema", line: 1, message: "portal/v1"}},
		},
		{
			name:       "unknown top-level key",
			descriptor: head + "  - {id: orders-http, kind: openapi, spec: openapi.yaml, lifecycle: production}\nextra: 1\n",
			want:       []want{{rule: "descriptor-schema", line: 1, message: "extra"}},
		},
		{
			name:       "bad id pattern",
			descriptor: head + "  - id: Orders\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-schema", line: 4, message: "does not match pattern"}},
		},
		{
			name:       "sunset without deprecated",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: production\n    sunset: 2027-03-31\n",
			want:       []want{{rule: "descriptor-schema", line: 4, message: "sunset is only allowed"}},
		},
		{
			name:       "sunset with deprecated is fine",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: deprecated\n    sunset: 2027-03-31\n",
		},
		{
			name:       "bad sunset date",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: deprecated\n    sunset: next year\n",
			want:       []want{{rule: "descriptor-schema", line: 8, message: "date"}},
		},
		{
			name:       "compatibility on openapi",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: production\n    compatibility: FULL\n",
			want:       []want{{rule: "descriptor-schema", line: 4, message: "compatibility is only allowed"}},
		},
		{
			name:       "environment with url and broker",
			descriptor: head + "  - id: orders-events\n    kind: cloudevents\n    spec: events.yaml\n    lifecycle: production\n    environments:\n      - {name: prod, url: \"https://x.internal\", broker: kafka-prod}\n",
			want:       []want{{rule: "descriptor-schema", line: 9, message: "exactly one of url or broker"}},
		},
		{
			name:       "bad link url",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: openapi.yaml\n    lifecycle: production\n    links:\n      - {title: Runbook, url: \"not a url\"}\n",
			want:       []want{{rule: "descriptor-schema", line: 9, message: "uri"}},
		},
		{
			name: "duplicate id",
			descriptor: head + "  - {id: orders-http, kind: openapi, spec: openapi.yaml, lifecycle: production}\n" +
				"  - {id: orders-http, kind: cloudevents, spec: events.yaml, lifecycle: production}\n",
			want: []want{{rule: "descriptor-duplicate-id", line: 5, message: "apis[0]"}},
		},
		{
			name:       "missing spec file",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: nope.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-spec-path", line: 6, message: "does not exist"}},
		},
		{
			name:       "spec escapes the repo",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: ../openapi.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-spec-path", line: 6, message: "escapes"}},
		},
		{
			name:       "absolute spec path",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: /etc/openapi.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-spec-path", line: 6, message: "relative"}},
		},
		{
			name:       "kind does not match content",
			descriptor: head + "  - id: orders-events\n    kind: cloudevents\n    spec: openapi.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-kind-mismatch", line: 5, message: "openapi.yaml is openapi"}},
		},
		{
			name:       "unrecognised spec",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: plain.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "descriptor-kind-mismatch", line: 5, message: "no openapi"}},
		},
		{
			name:       "asyncapi 3 is accepted",
			descriptor: head + "  - id: orders-async\n    kind: asyncapi\n    spec: asyncapi.yaml\n    lifecycle: production\n",
		},
		{
			name:       "swagger 2.0",
			descriptor: head + "  - id: orders-http\n    kind: openapi\n    spec: swagger.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "spec-unsupported-version", file: "swagger.yaml", line: 1, message: "convert it to OpenAPI"}},
		},
		{
			name:       "asyncapi 2.x",
			descriptor: head + "  - id: orders-async\n    kind: asyncapi\n    spec: asyncapi2.yaml\n    lifecycle: production\n",
			want:       []want{{rule: "spec-unsupported-version", file: "asyncapi2.yaml", line: 3, message: "upgrade to AsyncAPI 3.0"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The descriptor lives one level down so that ../ stays inside the temp dir.
			dir := filepath.Join(t.TempDir(), "repo")
			files := map[string]string{"portal.yaml": tt.descriptor}
			for k, v := range specs {
				files[k] = v
			}
			files["../openapi.yaml"] = specs["openapi.yaml"]
			for name, content := range files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			descPath := filepath.Join(dir, "portal.yaml")
			_, got, err := Load(descPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tt.want), dump(got))
			}
			for i, w := range tt.want {
				g := got[i]
				wantFile := descPath
				if w.file != "" {
					wantFile = filepath.Join(dir, w.file)
				}
				if g.RuleID != w.rule || g.Severity != model.SeverityError || g.File != wantFile ||
					g.Line != w.line || !strings.Contains(g.Message, w.message) {
					t.Errorf("finding %d = %+v\nwant rule=%s file=%s line=%d message~%q", i, g, w.rule, wantFile, w.line, w.message)
				}
			}
		})
	}
}

func dump(fs []model.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  %s line %d %s: %s\n", f.RuleID, f.Line, f.Pointer, f.Message)
	}
	return b.String()
}
