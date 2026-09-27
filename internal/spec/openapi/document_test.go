package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/bundle"
)

func TestDocumentInternalizesFileRefs(t *testing.T) {
	b := &bundle.Bundle{Entry: "api/openapi.yaml", Files: map[string][]byte{
		"api/openapi.yaml": []byte(`openapi: 3.0.3
info: {title: Orders, version: 1.0.0}
paths:
  /orders/{id}:
    get:
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "200":
          description: The order.
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Order"}
components:
  schemas:
    Order:
      type: object
      properties:
        total: {$ref: "./schemas/money.json"}
`),
		"api/schemas/money.json":    []byte(`{"type": "object", "properties": {"currency": {"$ref": "currency.json"}, "amount": {"type": "integer"}}}`),
		"api/schemas/currency.json": []byte(`{"type": "string", "pattern": "^[A-Z]{3}$"}`),
	}}
	out, err := Document(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, ".json") || strings.Contains(s, "/bundle/") {
		t.Errorf("file references remain:\n%s", s)
	}
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Components.Schemas) < 3 {
		t.Errorf("schemas = %v, want Order plus the two files'", keys(doc.Components.Schemas))
	}
	if !strings.Contains(s, `"^[A-Z]{3}$"`) {
		t.Errorf("the nested file's content is missing:\n%s", s)
	}
}

func TestDocumentStaysInTheBundle(t *testing.T) {
	for name, ref := range map[string]string{
		"remote": "https://example.com/x.json",
		"escape": "../../etc/passwd",
		"absent": "./nope.json",
	} {
		b := &bundle.Bundle{Entry: "openapi.yaml", Files: map[string][]byte{"openapi.yaml": []byte(`openapi: 3.0.3
info: {title: x, version: 1.0.0}
paths: {}
components: {schemas: {X: {$ref: "` + ref + `"}}}
`)}}
		if _, err := Document(b); err == nil {
			t.Errorf("%s ref %s: expected an error", name, ref)
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDocumentOpenAPI31(t *testing.T) {
	b := &bundle.Bundle{Entry: "openapi.yaml", Files: map[string][]byte{"openapi.yaml": []byte(`openapi: 3.1.0
info: {title: x, version: 1.0.0, summary: A 3.1 field}
paths:
  /x:
    get:
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: object
                properties:
                  note: {type: [string, "null"], examples: [hi]}
webhooks:
  ping:
    post:
      responses: {"200": {description: ok}}
`)}}
	out, err := Document(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"openapi":"3.1.0"`, `"null"`, `"webhooks"`, `"summary":"A 3.1 field"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}
}
