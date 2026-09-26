package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/openapi"
)

// cleanOAS passes every native rule.
const cleanOAS = `openapi: 3.1.0
info:
  title: T
  version: 1.0.0
  contact: {name: team}
servers:
  - url: https://t.prod.internal
security: [{bearer: []}]
paths:
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string, maxLength: 64}}
      responses:
        '200':
          description: ok
          content: {application/json: {schema: {$ref: '#/components/schemas/Thing'}}}
        '401':
          description: no
          content: {application/problem+json: {schema: {type: object}}}
components:
  securitySchemes:
    bearer: {type: http, scheme: bearer}
  schemas:
    Thing:
      type: object
      properties:
        name: {type: string, format: hostname}
        count: {type: integer, format: int32, minimum: 0}
`

func parseOAS(t *testing.T, doc string) OpenAPITarget {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "openapi.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, fs, err := openapi.Parse(root, p)
	if err != nil || res == nil || len(fs) > 0 {
		t.Fatalf("fixture doesn't parse: %v %+v", err, fs)
	}
	return OpenAPITarget{Spec: res.Spec, Doc: res.Doc, Raw: res.Raw, Bytes: res.Bytes, File: p, Dir: root,
		Environments: []string{"https://t.prod.internal"}}
}

// native runs only the portal's own rules, so assertions don't depend on
// vacuum's.
func native(t OpenAPITarget) []model.Finding {
	return run(openAPIRules, Target{Spec: t.Spec, File: t.File, Line: t.Doc.Line, openapi: &t}, Config{})
}

func TestOpenAPINativeRules(t *testing.T) {
	tests := []struct {
		name  string
		edits [][2]string
		envs  []string // overrides the environments when set
		want  []want
	}{
		{name: "clean"},

		{name: "operationId not camelCase", edits: [][2]string{{"getThing", "get_thing"}},
			want: []want{{"org-operation-id", model.SeverityError, 12, "must be camelCase"}}},
		{name: "no contact", edits: [][2]string{{"  contact: {name: team}\n", ""}},
			want: []want{{"org-owner-contact", model.SeverityWarn, 2, "info.contact"}}},
		{name: "error response not problem+json", edits: [][2]string{{"application/problem+json: {schema", "application/json: {schema"}},
			want: []want{{"org-problem-json", model.SeverityWarn, 19, "401 response"}}},
		{name: "version in path", edits: [][2]string{{"/things/{id}", "/v1/things/{id}"}},
			want: []want{{"org-no-version-in-path", model.SeverityWarn, 10, "contains the version v1"}}},
		{name: "server not an environment", envs: []string{"https://t.staging.internal"},
			want: []want{{"org-servers-match-env", model.SeverityWarn, 7, "not one of the environments"}}},
		{name: "no environments declared", envs: []string{}},

		{name: "public without justification", edits: [][2]string{{"      operationId: getThing\n", "      operationId: getThing\n      security: []\n"}},
			want: []want{{"sec-operation-security", model.SeverityError, 11, "justify it with x-portal-public"}}},
		{name: "public with justification", edits: [][2]string{{"      operationId: getThing\n",
			"      operationId: getThing\n      security: []\n      x-portal-public: health check for the load balancer\n"}}},
		{name: "justification without an explicit security: []",
			edits: [][2]string{{"security: [{bearer: []}]\n", ""}, {"      operationId: getThing\n", "      operationId: getThing\n      x-portal-public: why\n"}},
			want:  []want{{"sec-operation-security", model.SeverityError, 10, "has no security"}}},
		{name: "optional auth counts as public", edits: [][2]string{{"security: [{bearer: []}]", "security: [{bearer: []}, {}]"}},
			want: []want{{"sec-operation-security", model.SeverityError, 11, "has no security"}}},

		{name: "api key in query", edits: [][2]string{{"bearer: {type: http, scheme: bearer}", "bearer: {type: apiKey, in: query, name: key}"}},
			want: []want{{"sec-no-query-credentials", model.SeverityError, 24, "query string"}}},
		{name: "basic auth", edits: [][2]string{{"scheme: bearer}", "scheme: Basic}"}},
			want: []want{{"sec-no-basic-auth", model.SeverityError, 24, "basic"}}},
		{name: "http server", edits: [][2]string{{"https://t.prod.internal", "http://t.prod.internal"}}, envs: []string{"http://t.prod.internal"},
			want: []want{{"sec-https-servers", model.SeverityError, 7, "use https"}}},
		{name: "http localhost is fine", edits: [][2]string{{"https://t.prod.internal", "http://localhost:8080"}}, envs: []string{}},

		{name: "unrestricted string", edits: [][2]string{{"name: {type: string, format: hostname}", "name: {type: string}"}},
			want: []want{{"sec-string-restricted", model.SeverityWarn, 29, "no maxLength"}}},
		{name: "enum restricts a string", edits: [][2]string{{"name: {type: string, format: hostname}", "name: {type: string, enum: [a, b]}"}}},
		{name: "string inside allOf", edits: [][2]string{{"    Thing:\n      type: object\n", "    Thing:\n      allOf: [{type: string}]\n      type: object\n"}},
			want: []want{{"sec-string-restricted", model.SeverityWarn, 27, "no maxLength"}}},
		{name: "unbounded integer", edits: [][2]string{{"count: {type: integer, format: int32, minimum: 0}", "count: {type: integer}"}},
			want: []want{{"sec-integer-bounds", model.SeverityWarn, 30, "no format and minimum or maximum"}}},

		{name: "secured operation without 401", edits: [][2]string{{"        '401':\n          description: no\n          content: {application/problem+json: {schema: {type: object}}}\n", ""}},
			want: []want{{"sec-auth-responses", model.SeverityWarn, 15, "no 401"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := cleanOAS
			for _, e := range tt.edits {
				if !strings.Contains(doc, e[0]) {
					t.Fatalf("fixture has no %q", e[0])
				}
				doc = strings.Replace(doc, e[0], e[1], 1)
			}
			target := parseOAS(t, doc)
			if tt.envs != nil {
				target.Environments = tt.envs
			}
			got := native(target)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d findings, want %d:\n%s", len(got), len(tt.want), dump(got))
			}
			for i, w := range tt.want {
				g := got[i]
				if g.RuleID != w.rule || g.Severity != w.severity || g.Line != w.line || !strings.Contains(g.Message, w.message) {
					t.Errorf("finding %d = %+v\nwant %+v", i, g, w)
				}
			}
		})
	}
}

func TestVacuumSmoke(t *testing.T) {
	// No operationId: vacuum's recommended operation-operationId is an error.
	target := parseOAS(t, strings.Replace(cleanOAS, "      operationId: getThing\n", "", 1))
	var found bool
	for _, f := range OpenAPI(target, Config{}) {
		if f.RuleID == "operation-operationId" {
			found = true
			if f.Severity != model.SeverityError || f.Line != 11 || f.Pointer != "/paths/~1things~1{id}/get" {
				t.Errorf("finding = %+v", f)
			}
		}
	}
	if !found {
		t.Error("vacuum didn't report operation-operationId")
	}
}

func TestVacuumDisabledRules(t *testing.T) {
	// cleanOAS has no examples and no component descriptions.
	for _, f := range OpenAPI(parseOAS(t, cleanOAS), Config{}) {
		for _, id := range disabledVacuumRules {
			if f.RuleID == id {
				t.Errorf("disabled rule fired: %+v", f)
			}
		}
	}
}

func TestJSONPathToPointer(t *testing.T) {
	tests := map[string]string{
		"$":                             "",
		"$.info":                        "/info",
		"$.paths['/a/{b}'].get":         "/paths/~1a~1{b}/get",
		"$.servers[0].url":              "/servers/0/url",
		"$.components.schemas['A.B']":   "/components/schemas/A.B",
		"$.x['application/json'].y[12]": "/x/application~1json/y/12",
	}
	for in, want := range tests {
		got, ok := jsonPathToPointer(in)
		if !ok || got != want {
			t.Errorf("%s → %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"paths", "$..x", "$[x]", "$['open"} {
		if _, ok := jsonPathToPointer(bad); ok {
			t.Errorf("%s converted", bad)
		}
	}
}
