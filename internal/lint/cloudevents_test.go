package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/eventcatalog"
)

var acme = Config{EventTypePrefix: "com.acme."}

// lintEvents parses api/events.yaml (plus extra files) from a fresh root and
// lints it, so pointers and lines are the real ones.
func lintEvents(t *testing.T, events string, extra map[string]string, cfg Config) []model.Finding {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{"api/events.yaml": events}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, fs, err := eventcatalog.Parse(root, filepath.Join(root, "api/events.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) > 0 {
		t.Fatalf("fixture doesn't parse cleanly: %+v", fs)
	}
	return CloudEvents(Target{Spec: res.Spec, Payloads: res.Payloads, File: res.Doc.Path, Line: res.Doc.Line}, cfg)
}

// clean passes every rule; lines 8–12 are the message.
const clean = `eventcatalog: "1.0"
title: T
version: 1.0.0
defaults:
  bindings:
    - kafka: {topic: t.events, key: {from: subject}}
messages:
  - type: com.acme.t.happened.v1
    role: produces
    summary: S
    description: D
    dataschema: {schema: {type: object}}
`

type want struct {
	rule     string
	severity model.Severity
	line     int
	message  string // substring
}

func TestCloudEvents(t *testing.T) {
	tests := []struct {
		name   string
		events string
		extra  map[string]string
		cfg    Config
		want   []want
	}{
		{name: "clean", events: clean, cfg: acme},

		{
			name:   "type prefix",
			events: strings.Replace(clean, "com.acme.t", "com.other.t", 1),
			cfg:    acme,
			want:   []want{{"ce-type-prefix", model.SeverityError, 8, "must start with com.acme."}},
		},
		{
			name:   "type prefix skipped without config",
			events: strings.Replace(clean, "com.acme.t", "com.other.t", 1),
		},

		{
			name: "valid example",
			events: clean + `    examples:
      - data: {anything: 1}
`,
			cfg: acme,
		},
		{
			name: "invalid example points at the field",
			events: strings.Replace(clean, "{schema: {type: object}}",
				"{schema: {type: object, properties: {total: {type: object, properties: {amount: {type: integer}}}}}}", 1) +
				`    examples:
      - name: bad
        data:
          total:
            amount: "12"
`,
			cfg:  acme,
			want: []want{{"ce-examples-valid", model.SeverityError, 17, "example bad does not match the dataschema: got string, want integer"}},
		},

		{
			name: "no bindings",
			events: `eventcatalog: "1.0"
title: T
version: 1.0.0
messages:
  - type: com.acme.t.happened.v1
    role: produces
    summary: S
    description: D
    dataschema: {schema: {}}
`,
			cfg:  acme,
			want: []want{{"ce-binding-present", model.SeverityError, 5, "has no bindings"}},
		},

		{
			name: "kafka key missing on a shared default is reported once",
			events: strings.Replace(clean, ", key: {from: subject}", "", 1) + `  - type: com.acme.t.other.v1
    role: produces
    summary: S
    description: D
    dataschema: {schema: {}}
`,
			cfg:  acme,
			want: []want{{"ce-kafka-key", model.SeverityWarn, 6, "kafka binding to t.events has no key"}},
		},

		{
			name:   "major matches file name",
			events: strings.Replace(clean, "{schema: {type: object}}", "{$ref: ./happened.v1.json}", 1),
			extra:  map[string]string{"api/happened.v1.json": `{"type": "object"}`},
			cfg:    acme,
		},
		{
			name:   "major differs from file name",
			events: strings.Replace(clean, "{schema: {type: object}}", "{$ref: ./happened.v2.json}", 1),
			extra:  map[string]string{"api/happened.v2.json": `{"type": "object"}`},
			cfg:    acme,
			want:   []want{{"ce-type-major-matches-schema", model.SeverityWarn, 12, "declares v2"}},
		},
		{
			name:   "major differs from $id",
			events: strings.Replace(clean, "{schema: {type: object}}", "{$ref: ./happened.json}", 1),
			extra:  map[string]string{"api/happened.json": `{"$id": "https://schemas.acme.test/t/happened/v3", "type": "object"}`},
			cfg:    acme,
			want:   []want{{"ce-type-major-matches-schema", model.SeverityWarn, 12, "$id https://schemas.acme.test/t/happened/v3 declares v3"}},
		},
		{
			name:   "no declared major",
			events: strings.Replace(clean, "{schema: {type: object}}", "{$ref: ./happened.json}", 1),
			extra:  map[string]string{"api/happened.json": `{"type": "object"}`},
			cfg:    acme,
		},

		{
			name:   "receives needs no description",
			events: strings.Replace(strings.Replace(clean, "    description: D\n", "", 1), "role: produces", "role: receives", 1),
			cfg:    acme,
		},
		{
			name:   "produces needs a description",
			events: strings.Replace(clean, "    description: D\n", "", 1),
			cfg:    acme,
			want:   []want{{"ce-description", model.SeverityWarn, 8, "has no description"}},
		},

		{
			name:   "json with parameters",
			events: clean + "    datacontenttype: application/json; charset=utf-8\n",
			cfg:    acme,
		},
		{
			name:   "structured json suffix",
			events: clean + "    datacontenttype: application/cloudevents+json\n",
			cfg:    acme,
		},
		{
			name:   "avro",
			events: clean + "    datacontenttype: application/avro\n",
			cfg:    acme,
			want:   []want{{"ce-json-only", model.SeverityError, 8, "application/avro is not supported"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lintEvents(t, tt.events, tt.extra, tt.cfg)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tt.want), dump(got))
			}
			for i, w := range tt.want {
				g := got[i]
				if g.RuleID != w.rule || g.Severity != w.severity || g.Line != w.line || !strings.Contains(g.Message, w.message) ||
					!strings.HasSuffix(g.File, "api/events.yaml") {
					t.Errorf("finding %d = %+v\nwant %+v", i, g, w)
				}
			}
		})
	}
}

// ce-type-format can't fail for event catalogues, whose schema enforces the
// same pattern, so it's tested on a hand-built model as other sources will be.
func TestTypeFormat(t *testing.T) {
	spec := &model.Spec{Messages: []model.Message{
		{Key: "com.acme.t.ok.v1", Pointer: "/messages/0"},
		{Key: "OrderCreated", Pointer: "/messages/1"},
		{Key: "com.acme.t.no-version", Pointer: "/messages/2"},
	}}
	var got []string
	typeFormat(Target{Spec: spec}, Config{}, func(ptr, _ string) { got = append(got, ptr) })
	if strings.Join(got, ",") != "/messages/1/type,/messages/2/type" {
		t.Errorf("reported %v", got)
	}
}

func TestScore(t *testing.T) {
	f := func(n int, s model.Severity) []model.Finding {
		fs := make([]model.Finding, n)
		for i := range fs {
			fs[i].Severity = s
		}
		return fs
	}
	tests := []struct {
		findings []model.Finding
		want     int
	}{
		{nil, 100},
		{append(f(3, model.SeverityError), f(2, model.SeverityWarn)...), 66},
		{f(15, model.SeverityError), 0},
		{f(5, model.SeverityInfo), 100},
	}
	for _, tt := range tests {
		if got := Score(tt.findings); got != tt.want {
			t.Errorf("Score(%d findings) = %d, want %d", len(tt.findings), got, tt.want)
		}
	}
}

func dump(fs []model.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  %s %s line %d %s: %s\n", f.Severity, f.RuleID, f.Line, f.Pointer, f.Message)
	}
	return b.String()
}
