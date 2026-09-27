package eventcatalog

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestParseExampleGolden(t *testing.T) {
	root := "../../../docs/spec/examples/orders-service"
	res, findings, err := Parse(root, filepath.Join(root, "api/events.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if len(res.Payloads) != len(res.Spec.Messages) {
		t.Errorf("got %d compiled payloads for %d messages", len(res.Payloads), len(res.Spec.Messages))
	}
	got, err := json.MarshalIndent(res.Spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := "testdata/orders-events.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("model differs from %s (run go test -update to accept):\n%s", golden, got)
	}
}

// setup writes files under a fresh root and parses api/events.yaml, so that
// ../../ in a $ref leaves the root.
func setup(t *testing.T, files map[string]string) (string, *Result, []model.Finding) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, findings, err := Parse(root, filepath.Join(root, "api/events.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return root, res, findings
}

// eventsYAML returns an events.yaml whose single message (on line 6) has the
// given dataschema and extra lines.
func eventsYAML(dataschema string, extra ...string) string {
	return `eventcatalog: "1.0"
title: T
version: 1.0.0
defaults: {bindings: [{kafka: {topic: t.events}}]}
messages:
  - type: com.acme.t.happened.v1
    role: produces
    summary: S
    dataschema: ` + dataschema + "\n" + strings.Join(extra, "\n")
}

const objectSchema = `{"type": "object"}`

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		rule    string
		line    int
		message string
	}{
		{
			name:    "yaml syntax error",
			files:   map[string]string{"api/events.yaml": "title: [unclosed\n"},
			rule:    "eventcatalog-syntax",
			line:    1,
			message: "not valid YAML",
		},
		{
			name:    "bad type pattern",
			files:   map[string]string{"api/events.yaml": strings.Replace(eventsYAML("{schema: {}}"), "happened.v1", "Happened", 1)},
			rule:    "eventcatalog-schema",
			line:    6,
			message: "does not match pattern",
		},
		{
			name: "duplicate type",
			files: map[string]string{"api/events.yaml": eventsYAML("{schema: {}}",
				"  - type: com.acme.t.happened.v1", "    role: produces", "    summary: S", "    dataschema: {schema: {}}")},
			rule:    "eventcatalog-duplicate-type",
			line:    10,
			message: "messages[0]",
		},
		{
			name:    "missing ref file",
			files:   map[string]string{"api/events.yaml": eventsYAML("{$ref: ./nope.json}")},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "api/nope.json does not exist",
		},
		{
			name: "nested ref to missing file",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"properties": {"b": {"$ref": "./b.json"}}}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "api/b.json does not exist",
		},
		{
			name:    "remote ref",
			files:   map[string]string{"api/events.yaml": eventsYAML("{$ref: \"https://example.com/s.json\"}")},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "remote $ref",
		},
		{
			name: "nested remote ref",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"properties": {"b": {"$ref": "https://example.com/b.json"}}}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "remote $ref",
		},
		{
			name:    "ref escapes the root",
			files:   map[string]string{"api/events.yaml": eventsYAML("{$ref: ../../outside.json}")},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "leaves the descriptor's directory",
		},
		{
			name: "nested ref escapes the root",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"properties": {"b": {"$ref": "../../outside.json"}}}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "a $ref leads to ../outside.json, which leaves",
		},
		{
			name: "draft-04",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"$schema": "http://json-schema.org/draft-04/schema#", "type": "object"}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "draft-04/schema# is not supported",
		},
		{
			name: "malformed schema file",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"type": `,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "api/a.json: yaml: line 1",
		},
		{
			name: "invalid schema",
			files: map[string]string{
				"api/events.yaml": eventsYAML("{$ref: ./a.json}"),
				"api/a.json":      `{"properties": {"x": {"type": "thing"}}}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "api/a.json is not a valid JSON Schema: /properties/x/type:",
		},
		{
			name:    "invalid inline schema",
			files:   map[string]string{"api/events.yaml": eventsYAML("{schema: {type: thing}}")},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: "api/events.yaml#/messages/0/dataschema/schema is not a valid JSON Schema: /type:",
		},
		{
			name: "ref fragment not found",
			files: map[string]string{
				"api/events.yaml": eventsYAML(`{$ref: "./a.json#/$defs/nope"}`),
				"api/a.json":      `{}`,
			},
			rule:    "ce-dataschema-resolves",
			line:    9,
			message: `"api/a.json#/$defs/nope" not found`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, got := setup(t, tt.files)
			if len(got) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(got), got)
			}
			g := got[0]
			if g.RuleID != tt.rule || g.Severity != model.SeverityError || g.Line != tt.line ||
				!strings.Contains(g.Message, tt.message) || !strings.HasSuffix(g.File, "api/events.yaml") {
				t.Errorf("finding = %+v\nwant rule=%s line=%d message~%q", g, tt.rule, tt.line, tt.message)
			}
		})
	}
}

func TestParseModel(t *testing.T) {
	t.Run("cyclic ref is accepted", func(t *testing.T) {
		_, res, fs := setup(t, map[string]string{
			"api/events.yaml":       eventsYAML("{$ref: ./schemas/node.json}"),
			"api/schemas/node.json": `{"type": "object", "properties": {"child": {"$ref": "#"}, "peer": {"$ref": "./peer.json"}}}`,
			"api/schemas/peer.json": `{"type": "object", "properties": {"back": {"$ref": "./node.json"}}}`,
		})
		if len(fs) > 0 {
			t.Fatalf("unexpected findings: %+v", fs)
		}
		want := []string{"api/events.yaml", "api/schemas/node.json", "api/schemas/peer.json"}
		if strings.Join(res.Spec.Files, ",") != strings.Join(want, ",") {
			t.Errorf("files = %v, want %v", res.Spec.Files, want)
		}
	})

	t.Run("inline schema with relative ref", func(t *testing.T) {
		_, res, fs := setup(t, map[string]string{
			"api/events.yaml": eventsYAML(`{schema: {type: object, properties: {total: {$ref: ./money.json}}}}`),
			"api/money.json":  `{"$schema": "http://json-schema.org/draft-07/schema#", "type": "integer"}`,
		})
		if len(fs) > 0 {
			t.Fatalf("unexpected findings: %+v", fs)
		}
		m := res.Spec.Messages[0]
		if m.Payload != "api/events.yaml#/messages/0/dataschema/schema" {
			t.Errorf("payload = %q", m.Payload)
		}
		if s := res.Spec.Schemas["api/money.json"]; s == nil || s.Draft != "07" {
			t.Errorf("money.json schema = %+v", s)
		}
		if err := res.Payloads[m.Key].Validate(map[string]any{"total": "12"}); err == nil {
			t.Error("payload schema accepted a string total; the $ref wasn't applied")
		}
		if strings.Join(res.Spec.Files, ",") != "api/events.yaml,api/money.json" {
			t.Errorf("files = %v", res.Spec.Files)
		}
	})

	t.Run("ref with fragment", func(t *testing.T) {
		_, res, fs := setup(t, map[string]string{
			"api/events.yaml": eventsYAML(`{$ref: "./a.json#/$defs/Evt"}`),
			"api/a.json":      `{"$defs": {"Evt": {"type": "object", "required": ["id"]}}}`,
		})
		if len(fs) > 0 {
			t.Fatalf("unexpected findings: %+v", fs)
		}
		m := res.Spec.Messages[0]
		if m.Payload != "api/a.json#/$defs/Evt" {
			t.Errorf("payload = %q", m.Payload)
		}
		if res.Payloads[m.Key].Validate(map[string]any{}) == nil {
			t.Error("payload schema is not the fragment: it accepted an object without id")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		_, res, fs := setup(t, map[string]string{
			"api/events.yaml": eventsYAML("{schema: {}}",
				"  - type: com.acme.t.other.v1",
				"    role: receives",
				"    summary: S",
				"    source: /own",
				"    datacontenttype: application/cloudevents+json",
				"    dataschema: {schema: {}}",
				"    bindings: [{nats: {subject: t.cmd, stream: T}}, {servicebus: {queue: q}}]",
			),
		})
		if len(fs) > 0 {
			t.Fatalf("unexpected findings: %+v", fs)
		}
		first, second := res.Spec.Messages[0], res.Spec.Messages[1]
		if first.CE.DataContentType != "application/json" {
			t.Errorf("datacontenttype fallback = %q", first.CE.DataContentType)
		}
		if len(first.Bindings) != 1 || first.Bindings[0].Protocol != "kafka" || first.Bindings[0].Address != "t.events" ||
			first.Bindings[0].Props["mode"] != "binary" || first.Bindings[0].Pointer != "/defaults/bindings/0" {
			t.Errorf("default bindings = %+v", first.Bindings)
		}
		if second.CE.Source != "/own" || second.CE.DataContentType != "application/cloudevents+json" {
			t.Errorf("own attributes = %+v", second.CE)
		}
		wantB := []model.Binding{
			{Protocol: "nats", Address: "t.cmd", Props: map[string]any{"stream": "T"}, Pointer: "/messages/1/bindings/0"},
			{Protocol: "servicebus", Address: "q", Pointer: "/messages/1/bindings/1"},
		}
		if b, _ := json.Marshal(second.Bindings); string(b) != mustJSON(t, wantB) {
			t.Errorf("own bindings = %s, want %s (they replace the defaults)", b, mustJSON(t, wantB))
		}
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
