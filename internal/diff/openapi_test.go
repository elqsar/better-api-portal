package diff

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/model"
	"better-api-portal/internal/spec/openapi"
)

// baseOAS has a request body, a query parameter and response enums for the
// governance table's rows to edit.
const baseOAS = `openapi: 3.1.0
info: {title: T, version: 1.0.0}
paths:
  /things/{id}:
    get:
      operationId: getThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
        - {name: verbose, in: query, schema: {type: boolean}}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Thing'}
    put:
      operationId: putThing
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: {type: string}
                size: {type: string, enum: [s, m, l]}
      responses:
        '204': {description: ok}
  /other:
    get:
      operationId: getOther
      responses:
        '204': {description: ok}
components:
  schemas:
    Thing:
      type: object
      required: [name, colour]
      properties:
        name: {type: string}
        colour: {type: string, enum: [red, green]}
        count: {type: integer}
`

func parseOAS(t *testing.T, dir, doc string) *openapi.Result {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	res, fs, err := openapi.Parse(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(fs) > 0 {
		t.Fatalf("fixture doesn't parse cleanly: %+v\n%s", fs, doc)
	}
	return res
}

func diffOAS(t *testing.T, edit [][2]string) []model.Change {
	t.Helper()
	root := t.TempDir()
	old := parseOAS(t, filepath.Join(root, "old"), baseOAS)
	new := parseOAS(t, filepath.Join(root, "new"), replace(t, baseOAS, edit))
	changes, err := OpenAPI(old, new)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// TestOpenAPI covers the rows of the 04-governance OpenAPI table.
func TestOpenAPI(t *testing.T) {
	tests := []struct {
		name string
		edit [][2]string
		want string // "rule impact" that must be among the changes
	}{
		{"remove an operation", [][2]string{{"  /other:\n    get:\n      operationId: getOther\n      responses:\n        '204': {description: ok}\n", ""}},
			"api-path-removed-without-deprecation breaking"},
		{"add a required parameter", [][2]string{{"{name: verbose, in: query, schema:", "{name: verbose, in: query, required: true, schema:"}},
			"request-parameter-became-required breaking"},
		{"add a required request field", [][2]string{{"required: [name]\n", "required: [name, size]\n"}},
			"request-property-became-required breaking"},
		{"remove a response field", [][2]string{{"        count: {type: integer}\n", ""}},
			"response-optional-property-removed breaking"},
		{"make a required response field optional", [][2]string{{"required: [name, colour]", "required: [name]"}},
			"response-property-became-optional breaking"},
		{"change a field's type", [][2]string{{"count: {type: integer}", "count: {type: string}"}},
			"response-property-type-changed breaking"},
		{"narrow a request enum", [][2]string{{"enum: [s, m, l]", "enum: [s, m]"}},
			"request-property-enum-value-removed breaking"},
		{"add an optional request field", [][2]string{{"                size:", "                note: {type: string}\n                size:"}},
			"new-optional-request-property additive"},
		{"add an operation", [][2]string{{"  /other:\n", "  /more:\n    get:\n      operationId: getMore\n      responses:\n        '204': {description: ok}\n  /other:\n"}},
			"endpoint-added additive"},
		{"add a response field", [][2]string{{"        count: {type: integer}\n", "        count: {type: integer}\n        note: {type: string}\n"}},
			"response-optional-property-added additive"},
		{"add a response enum value", [][2]string{{"enum: [red, green]", "enum: [red, green, blue]"}},
			"response-property-enum-value-added warn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range diffOAS(t, tt.edit) {
				got = append(got, c.RuleID+" "+string(c.Impact))
				if versioningChecks[c.RuleID] {
					t.Errorf("oasdiff's version check leaked through: %+v", c)
				}
				if c.Impact == model.ImpactBreaking && !strings.HasPrefix(c.ID, "BRK-OA-") {
					t.Errorf("breaking change without an id: %+v", c)
				}
				if c.Impact != model.ImpactBreaking && c.ID != "" {
					t.Errorf("non-breaking change with an id: %+v", c)
				}
			}
			if !slices.Contains(got, tt.want) {
				t.Errorf("changes = %v, want %q among them", got, tt.want)
			}
		})
	}
}

func TestOpenAPINoChanges(t *testing.T) {
	if cs := diffOAS(t, nil); len(cs) != 0 {
		t.Errorf("changes = %+v, want none", cs)
	}
}

func TestOpenAPILocatesChanges(t *testing.T) {
	removed := diffOAS(t, [][2]string{{"  /other:\n    get:\n      operationId: getOther\n      responses:\n        '204': {description: ok}\n", ""}})
	if len(removed) != 1 {
		t.Fatalf("changes = %+v", removed)
	}
	// Gone from the new spec, so it points into the old one.
	c := removed[0]
	if !strings.HasSuffix(c.File, filepath.Join("old", "openapi.yaml")) || c.Pointer != "/paths/~1other/get" || c.Line != 33 {
		t.Errorf("removal located at %s %s:%d", c.File, c.Pointer, c.Line)
	}
	if c.Type != "GET /other" || c.Kind != "removed" || c.Target != "operation" {
		t.Errorf("change = %+v", c)
	}

	changed := diffOAS(t, [][2]string{{"enum: [s, m, l]", "enum: [s, m]"}})
	if len(changed) != 1 || !strings.HasSuffix(changed[0].File, filepath.Join("new", "openapi.yaml")) ||
		changed[0].Pointer != "/paths/~1things~1{id}/put" || changed[0].Line != 16 {
		t.Errorf("changes = %+v", changed)
	}
}

func TestOpenAPIChangeIDsAreStable(t *testing.T) {
	edit := [][2]string{{"count: {type: integer}", "count: {type: string}"}}
	a, b := diffOAS(t, edit), diffOAS(t, edit)
	if len(a) == 0 || a[0].ID == "" || a[0].ID != b[0].ID {
		t.Errorf("ids differ or are missing: %+v vs %+v", a, b)
	}
}

// The loader only reads the bundle closure: a document whose closure has
// been tampered with after parsing can't pull in other files.
func TestOpenAPIReadsOnlyTheClosure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.yaml"), []byte("type: string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := parseOAS(t, filepath.Join(root, "old"), baseOAS)
	new := parseOAS(t, filepath.Join(root, "new"), baseOAS)
	new.Bytes = []byte(strings.Replace(baseOAS, "count: {type: integer}", "count: {$ref: '../secret.yaml'}", 1))
	_, err := OpenAPI(old, new)
	if err == nil || !strings.Contains(err.Error(), "not in the spec's file closure") {
		t.Errorf("err = %v, want a closure error", err)
	}
}
