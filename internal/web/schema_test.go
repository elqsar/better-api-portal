package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"better-api-portal/internal/model"
)

func specOf(t *testing.T, docs map[string]string) *model.Spec {
	t.Helper()
	s := &model.Spec{Schemas: map[string]*model.Schema{}}
	for k, v := range docs {
		var doc any
		if err := json.Unmarshal([]byte(v), &doc); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		s.Schemas[k] = &model.Schema{Pointer: k, Doc: doc}
	}
	return s
}

// outline prints a tree one node per line, indented by depth.
func outline(n *schemaNode, depth int, out *strings.Builder) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s %s", strings.Repeat("  ", depth), n.Name, n.Type)
	if n.Required {
		b.WriteString(" required")
	}
	if n.Ref != "" {
		b.WriteString(" ref=" + n.Ref)
	}
	for _, f := range n.Facts {
		b.WriteString(" [" + f + "]")
	}
	if n.Enum != nil {
		b.WriteString(" enum=" + strings.Join(n.Enum, ","))
	}
	if n.Note != "" {
		b.WriteString(" (" + n.Note + ")")
	}
	if n.Description != "" {
		b.WriteString(" « " + n.Description)
	}
	out.WriteString(strings.TrimRight(b.String(), " ") + "\n")
	for _, c := range n.Children {
		outline(c, depth+1, out)
	}
}

func treeOutline(t *testing.T, docs map[string]string, payload string) string {
	t.Helper()
	tree, err := schemaTree(specOf(t, docs), payload)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	outline(tree, 0, &b)
	return b.String()
}

func TestSchemaTree(t *testing.T) {
	got := treeOutline(t, map[string]string{
		"api/schemas/order.json": `{
			"type": "object", "required": ["id", "total"],
			"properties": {
				"id": {"type": "string", "format": "uuid"},
				"total": {"$ref": "./money.json", "description": "Including tax."},
				"lines": {"type": "array", "minItems": 1, "items": {"$ref": "#/$defs/line"}},
				"status": {"enum": ["open", "closed"], "default": "open"},
				"payment": {"oneOf": [{"$ref": "#/$defs/card"}, {"type": "null"}]},
				"meta": {"type": "object", "additionalProperties": {"type": "string"}}
			},
			"additionalProperties": false,
			"$defs": {
				"line": {"type": "object", "properties": {"qty": {"type": "integer", "minimum": 1}}},
				"card": {"type": "object", "properties": {"last4": {"type": "string", "pattern": "^[0-9]{4}$"}}}
			}
		}`,
		"api/schemas/money.json": `{"type": "object", "description": "An amount.", "properties": {"amount": {"type": "integer"}}}`,
	}, "api/schemas/order.json")
	want := ` object [no other properties]
  id string required [format: uuid]
  lines array [min items 1]
    items object ref=#/$defs/line
      qty integer [≥ 1]
  meta object
    other properties string
  payment
    one of
      #1 object ref=#/$defs/card
        last4 string [pattern: ^[0-9]{4}$]
      #2 null
  status  [default "open"] enum="open","closed"
  total object required ref=./money.json « Including tax.
    amount integer
`
	if got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestSchemaTreeRecursion(t *testing.T) {
	got := treeOutline(t, map[string]string{
		"api/events.yaml#/messages/0/dataschema/schema": `{
			"type": "object",
			"properties": {"name": {"type": "string"}, "children": {"type": "array", "items": {"$ref": "./node.json"}}}
		}`,
		"api/node.json": `{"type": "object", "properties": {"parent": {"$ref": "#"}, "missing": {"$ref": "./nope.json"}}}`,
	}, "api/events.yaml#/messages/0/dataschema/schema")
	want := ` object
  children array
    items object ref=./node.json
      missing  ref=./nope.json ($ref ./nope.json: api/nope.json is not in the bundle)
      parent object ref=# (recursive: see above)
  name string
`
	if got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestSchemaTreeLimits(t *testing.T) {
	// A chain deeper than the tree shows stops with a note.
	var b strings.Builder
	for range maxTreeDepth + 4 {
		b.WriteString(`{"type": "object", "properties": {"next": `)
	}
	b.WriteString(`{"type": "string"}`)
	for range maxTreeDepth + 4 {
		b.WriteString(`}}`)
	}
	got := treeOutline(t, map[string]string{"a.json": b.String()}, "a.json")
	if !strings.Contains(got, "(not shown: the schema is too large to show in full)") ||
		strings.Count(got, "\n") != maxTreeDepth+1 {
		t.Errorf("tree:\n%s", got)
	}
	if _, err := schemaTree(specOf(t, nil), "nope.json"); err == nil {
		t.Error("a payload outside the bundle is an error")
	}
}
