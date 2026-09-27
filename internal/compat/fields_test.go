package compat

import (
	"slices"
	"testing"
)

func TestFieldChanges(t *testing.T) {
	money := [2]string{"api/money.json", `{"type": "object", "properties": {"amount": {"type": "integer"}}}`}
	moneyTax := [2]string{"api/money.json", `{"type": "object", "properties": {"amount": {"type": "integer"}, "tax": {"type": "integer"}}}`}
	for _, c := range []struct {
		name     string
		old, new bundle
		want     []string // "path what (detail)"
	}{
		{"nothing", one(`{"type": "object"}`), one(`{"type": "object", "minProperties": 1}`), nil},
		{"added and removed",
			one(`{"properties": {"a": {}, "b": {}}, "required": ["a"]}`),
			one(`{"properties": {"a": {}, "c": {}, "d": {}}, "required": ["d"]}`),
			[]string{"/a now optional", "/b removed", "/c added (optional)", "/d added (required)"}},
		{"nested and array items",
			one(`{"properties": {"lines": {"type": "array", "items": {"properties": {"sku": {}}}}}}`),
			one(`{"properties": {"lines": {"type": "array", "items": {"properties": {"sku": {}, "note": {}}, "required": ["sku"]}}}}`),
			[]string{"/lines/[]/note added (optional)", "/lines/[]/sku now required"}},
		{"enum",
			one(`{"properties": {"s": {"enum": ["a", "b"]}}}`),
			one(`{"properties": {"s": {"enum": ["a", "c"]}}}`),
			[]string{`/s enum value added ("c")`, `/s enum value removed ("b")`}},
		{"through a ref to another file",
			bundle{files: [][2]string{{"api/s.json", `{"properties": {"total": {"$ref": "./money.json"}}}`}, money}},
			bundle{files: [][2]string{{"api/s.json", `{"properties": {"total": {"$ref": "./money.json"}}}`}, moneyTax}},
			[]string{"/total/tax added (optional)"}},
		{"recursive",
			one(`{"$defs": {"n": {"properties": {"next": {"$ref": "#/$defs/n"}}}}, "$ref": "#/$defs/n"}`),
			one(`{"$defs": {"n": {"properties": {"next": {"$ref": "#/$defs/n"}, "v": {}}}}, "$ref": "#/$defs/n"}`),
			[]string{"/v added (optional)"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fs, err := FieldChanges(c.old.schema(t), c.new.schema(t))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range fs {
				s := f.Path + " " + f.What
				if f.Detail != "" {
					s += " (" + f.Detail + ")"
				}
				got = append(got, s)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}
