package compat

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// supported keywords are the subset the checker reasons about
// (docs/spec/04-governance.md §2).
var supported = set("type", "enum", "const", "required", "properties", "additionalProperties",
	"items", "minimum", "maximum", "minLength", "maxLength", "pattern", "format", "$ref")

// annotations never affect which instances are valid.
var annotations = set("$schema", "$id", "$defs", "definitions", "$comment", "title", "description",
	"examples", "default", "deprecated", "readOnly", "writeOnly")

func set(ks ...string) map[string]bool {
	m := make(map[string]bool, len(ks))
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// unsupportedKeywords returns the keywords of m outside the subset, sorted.
func unsupportedKeywords(m map[string]any) []string {
	var ks []string
	for k := range m {
		if !supported[k] && !annotations[k] {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return ks
}

// constraints returns m without its annotations.
func constraints(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if !annotations[k] {
			out[k] = v
		}
	}
	return out
}

func containsRef(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		if _, ok := v["$ref"]; ok {
			return true
		}
		for _, c := range v {
			if containsRef(c) {
				return true
			}
		}
	case []any:
		for _, c := range v {
			if containsRef(c) {
				return true
			}
		}
	}
	return false
}

var allTypes = []string{"array", "boolean", "integer", "null", "number", "object", "string"}

// types returns the JSON types a schema allows, or nil for all of them. A
// schema without "type" but with enum or const allows the types of its values.
func types(m map[string]any) []string {
	switch t := m["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var ts []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				ts = append(ts, s)
			}
		}
		sort.Strings(ts)
		return ts
	}
	if vals := enumOf(m); vals != nil {
		var ts []string
		for _, v := range vals {
			if t := typeOf(v); !slices.Contains(ts, t) {
				ts = append(ts, t)
			}
		}
		sort.Strings(ts)
		return ts
	}
	return nil
}

// allows reports whether a type set (nil = all) admits t. integer is a
// subset of number.
func allows(ts []string, t string) bool {
	if ts == nil {
		return true
	}
	return slices.Contains(ts, t) || (t == "integer" && slices.Contains(ts, "number"))
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	if f, ok := toFloat(v); ok {
		if f == math.Trunc(f) {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

// enumOf returns the allowed values of a schema, or nil if unrestricted.
// const is a one-value enum.
func enumOf(m map[string]any) []any {
	if c, ok := m["const"]; ok {
		return []any{c}
	}
	if e, ok := m["enum"].([]any); ok {
		return e
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// norm makes JSON values comparable with reflect.DeepEqual regardless of how
// their numbers were decoded.
func norm(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, c := range v {
			out[k] = norm(c)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = norm(c)
		}
		return out
	}
	if f, ok := toFloat(v); ok {
		return f
	}
	return v
}

func equal(a, b any) bool { return reflect.DeepEqual(norm(a), norm(b)) }

func containsValue(vals []any, v any) bool {
	return slices.ContainsFunc(vals, func(x any) bool { return equal(x, v) })
}

// show renders a JSON value for messages.
func show(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(norm(v))
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func showTypes(ts []string) string {
	if ts == nil {
		return "any"
	}
	return strings.Join(ts, "|")
}
