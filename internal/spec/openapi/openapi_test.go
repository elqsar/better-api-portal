package openapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const example = "../../../docs/spec/examples/orders-service"

func TestParseExampleGolden(t *testing.T) {
	res, findings, err := Parse(example, filepath.Join(example, "api/openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	got, err := json.MarshalIndent(res.Spec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := "testdata/orders-http.golden.json"
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

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// doc is a minimal OpenAPI document whose one operation's response schema
// is given; its paths start on line 4.
func doc(version, schema string) string {
	return "openapi: 3.1.0\ninfo: {title: T, version: " + version + "}\nsecurity: [{bearer: []}]\npaths:\n" +
		"  /things:\n    get:\n      responses:\n        '200':\n          description: ok\n" +
		"          content: {application/json: {schema: " + schema + "}}\n" +
		"    post:\n      security: []\n      responses: {'204': {description: ok}}\n"
}

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		rule    string
		file    string
		line    int
		message string
	}{
		{name: "syntax", files: map[string]string{"api/openapi.yaml": "openapi: [3.1.0\n"},
			rule: "openapi-syntax", file: "api/openapi.yaml", line: 1},
		{name: "remote ref", files: map[string]string{"api/openapi.yaml": doc("1.0.0", `{$ref: "https://example.com/s.json"}`)},
			rule: "openapi-ref", file: "api/openapi.yaml", line: 10, message: "remote $ref"},
		{name: "missing file", files: map[string]string{"api/openapi.yaml": doc("1.0.0", `{$ref: ./nope.yaml}`)},
			rule: "openapi-ref", file: "api/openapi.yaml", line: 10, message: "api/nope.yaml does not exist"},
		{name: "escape", files: map[string]string{"api/openapi.yaml": doc("1.0.0", `{$ref: ../../outside.yaml}`)},
			rule: "openapi-ref", file: "api/openapi.yaml", line: 10, message: "leaves the descriptor's directory"},
		{name: "nested missing file", files: map[string]string{
			"api/openapi.yaml":   doc("1.0.0", `{$ref: ./schemas/a.yaml}`),
			"api/schemas/a.yaml": "type: object\nproperties:\n  b: {$ref: ./b.yaml}\n",
		}, rule: "openapi-ref", file: "api/schemas/a.yaml", line: 3, message: "api/schemas/b.yaml does not exist"},
		{name: "version not semver", files: map[string]string{"api/openapi.yaml": doc("v2", `{type: object}`)},
			rule: "openapi-version", file: "api/openapi.yaml", line: 2, message: `"v2" is not a semantic version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			_, fs, err := Parse(root, filepath.Join(root, "api/openapi.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) != 1 {
				t.Fatalf("findings = %+v", fs)
			}
			f := fs[0]
			if f.RuleID != tt.rule || f.File != filepath.Join(root, tt.file) || f.Line != tt.line || !strings.Contains(f.Message, tt.message) {
				t.Errorf("finding = %+v\nwant %s %s:%d ~%q", f, tt.rule, tt.file, tt.line, tt.message)
			}
		})
	}
}

func TestParseModel(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"api/openapi.yaml":   strings.Replace(doc("1.0.0", `{$ref: ./schemas/a.yaml}`), "openapi: 3.1.0", "openapi: 3.0.3", 1),
		"api/schemas/a.yaml": "type: object\nproperties:\n  b: {$ref: ./b.yaml}\n",
		"api/schemas/b.yaml": "type: string\n",
		"api/unrelated.yaml": "not: referenced\n",
	})
	res, fs, err := Parse(root, filepath.Join(root, "api/openapi.yaml"))
	if err != nil || len(fs) > 0 {
		t.Fatalf("err = %v, findings = %+v", err, fs)
	}
	s := res.Spec
	if want := []string{"api/openapi.yaml", "api/schemas/a.yaml", "api/schemas/b.yaml"}; !slices.Equal(s.Files, want) {
		t.Errorf("files = %v, want %v", s.Files, want)
	}
	if len(s.Operations) != 2 {
		t.Fatalf("operations = %+v", s.Operations)
	}
	get, post := s.Operations[0], s.Operations[1]
	if get.Responses["200"]["application/json"] != "api/schemas/a.yaml" {
		t.Errorf("response schema = %q", get.Responses["200"]["application/json"])
	}
	if !slices.Equal(get.Security, []string{"bearer"}) || len(post.Security) != 0 {
		t.Errorf("security: get %v, post %v (an explicit [] overrides the global requirement)", get.Security, post.Security)
	}
	if sch := s.Schemas["api/schemas/a.yaml"]; sch == nil || sch.Draft != "oas3.0" {
		t.Errorf("external schema = %+v", sch)
	}
}

func TestPathParametersAreInheritedAndOverridden(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"api/openapi.yaml": `openapi: 3.1.0
info: {title: T, version: 1.0.0}
paths:
  /things/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: string}}
      - {name: trace, in: header, schema: {type: string}}
    get:
      parameters:
        - {name: trace, in: header, required: true, schema: {type: string, maxLength: 64}}
      responses: {'204': {description: ok}}
`})
	res, fs, err := Parse(root, filepath.Join(root, "api/openapi.yaml"))
	if err != nil || len(fs) > 0 {
		t.Fatalf("err = %v, findings = %+v", err, fs)
	}
	ps := res.Spec.Operations[0].Parameters
	if len(ps) != 2 || ps[0].Name != "id" || !ps[1].Required ||
		ps[1].Schema != "api/openapi.yaml#/paths/~1things~1{id}/get/parameters/0/schema" {
		t.Errorf("parameters = %+v", ps)
	}
}
