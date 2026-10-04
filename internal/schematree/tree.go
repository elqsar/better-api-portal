// Package schematree turns a JSON Schema into a tree for people and agents
// to read: the event page's payload tree and the Markdown in agentdoc.
package schematree

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/elqsar/better-api-portal/internal/compat"
	"github.com/elqsar/better-api-portal/internal/model"
)

// Node is one line of a payload schema rendered as a tree: a property, an
// array's items, or a group such as "one of".
type Node struct {
	Name string
	// Label means Name describes the node's place ("items", "#1") rather
	// than naming a property.
	Label    bool
	Required bool
	// Group nodes ("one of") only hold their options; they aren't schemas.
	Group       bool
	Type        string
	Ref         string // the $ref followed to get here, as written
	Description string
	Facts       []string // format, bounds, pattern, default, …
	Enum        []string
	// Note explains why the node stops: a recursive ref, a ref that
	// doesn't resolve, or the size limits.
	Note     string
	Children []*Node
	Open     bool // expanded at first
}

// Limits that keep a huge or pathological schema from producing a huge page.
const (
	maxTreeDepth = 16
	maxTreeNodes = 1500
	openDepth    = 2 // levels expanded at first
)

type builder struct {
	s     compat.Schema
	nodes int
}

// FromSpec renders the payload schema at pointer payload of a spec parsed
// with its schema documents.
func FromSpec(spec *model.Spec, payload string) (*Node, error) {
	return Build(compat.FromSpec(spec, payload))
}

// Build renders the schema at s's root.
func Build(s compat.Schema) (*Node, error) {
	b := &builder{s: s}
	top, err := b.s.Top()
	if err != nil {
		return nil, err
	}
	root := &Node{}
	b.fill(root, top, nil, 0)
	return root, nil
}

// fill describes the schema at into n. path holds the keys of the schemas
// from the root to here, to spot recursion.
func (b *builder) fill(n *Node, at compat.Node, path []string, depth int) {
	b.nodes++
	n.Open = depth < openDepth
	// Follow refs. The referring schema's description wins: it's about this
	// use of the target.
	for hops := 0; ; hops++ {
		m, ok := at.Value.(map[string]any)
		if !ok {
			break
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			break
		}
		if n.Ref == "" {
			n.Ref = ref
		}
		if n.Description == "" {
			n.Description = str(m["description"])
		}
		if hops == maxTreeDepth {
			n.Note = "too many chained $refs"
			return
		}
		next, err := b.s.Follow(at, ref)
		if err != nil {
			n.Note = err.Error()
			return
		}
		at = next
	}
	if slices.Contains(path, at.Key()) {
		n.Note = "recursive: see above"
		n.Type = schemaType(at.Value)
		return
	}
	path = append(path, at.Key())

	switch v := at.Value.(type) {
	case bool:
		if v {
			n.Type = "any"
		} else {
			n.Note = "never valid"
		}
		return
	case map[string]any:
	default:
		n.Note = "not a schema"
		return
	}
	m := at.Value.(map[string]any)
	n.Type = schemaType(m)
	if n.Description == "" {
		n.Description = str(m["description"])
	}
	n.Facts = facts(m)
	if enum, ok := m["enum"].([]any); ok {
		for _, e := range enum {
			n.Enum = append(n.Enum, literal(e))
		}
	}

	var kids []*Node
	add := func(c *Node, v any, tokens ...string) {
		kids = append(kids, c)
		if depth+1 >= maxTreeDepth || b.nodes >= maxTreeNodes {
			c.Note = "not shown: the schema is too large to show in full"
			return
		}
		b.fill(c, at.Child(v, tokens...), path, depth+1)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		required := map[string]bool{}
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		for _, name := range sortedKeys(props) {
			add(&Node{Name: name, Required: required[name]}, props[name], "properties", name)
		}
	}
	if pp, ok := m["patternProperties"].(map[string]any); ok {
		for _, p := range sortedKeys(pp) {
			add(&Node{Name: "/" + p + "/", Label: true}, pp[p], "patternProperties", p)
		}
	}
	switch ap := m["additionalProperties"].(type) {
	case map[string]any:
		add(&Node{Name: "other properties", Label: true}, ap, "additionalProperties")
	case bool:
		if !ap {
			n.Facts = append(n.Facts, "no other properties")
		}
	}
	// Tuples: prefixItems (2020-12) or an items array (draft 07).
	tuple, _ := m["prefixItems"].([]any)
	tupleKey := "prefixItems"
	if items, ok := m["items"].([]any); ok {
		tuple, tupleKey = items, "items"
	}
	for i, it := range tuple {
		add(&Node{Name: "items[" + strconv.Itoa(i) + "]", Label: true}, it, tupleKey, strconv.Itoa(i))
	}
	if items, ok := m["items"].(map[string]any); ok {
		add(&Node{Name: "items", Label: true}, items, "items")
	}
	for _, kw := range []struct{ key, label string }{{"allOf", "all of"}, {"oneOf", "one of"}, {"anyOf", "any of"}} {
		opts, ok := m[kw.key].([]any)
		if !ok {
			continue
		}
		g := &Node{Name: kw.label, Label: true, Group: true, Open: depth+1 < openDepth}
		kids = append(kids, g)
		for i, o := range opts {
			c := &Node{Name: "#" + strconv.Itoa(i+1), Label: true}
			g.Children = append(g.Children, c)
			if depth+1 >= maxTreeDepth || b.nodes >= maxTreeNodes {
				c.Note = "not shown: the schema is too large to show in full"
				continue
			}
			b.fill(c, at.Child(o, kw.key, strconv.Itoa(i)), path, depth+1)
		}
	}
	if not, ok := m["not"]; ok {
		add(&Node{Name: "not", Label: true}, not, "not")
	}
	n.Children = kids
}

// schemaType is the type a schema declares, or the one its keywords imply.
func schemaType(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	switch t := m["type"].(type) {
	case string:
		if m["nullable"] == true { // OpenAPI 3.0
			return t + " | null"
		}
		return t
	case []any:
		var ts []string
		for _, x := range t {
			ts = append(ts, str(x))
		}
		return strings.Join(ts, " | ")
	}
	switch {
	case m["properties"] != nil:
		return "object"
	case m["items"] != nil || m["prefixItems"] != nil:
		return "array"
	case m["const"] != nil:
		return "const"
	}
	return ""
}

// facts lists a schema's constraints and annotations, in a fixed order.
func facts(m map[string]any) []string {
	var fs []string
	if f := str(m["format"]); f != "" {
		fs = append(fs, "format: "+f)
	}
	if p := str(m["pattern"]); p != "" {
		fs = append(fs, "pattern: "+p)
	}
	for _, k := range []struct{ key, text string }{
		{"minimum", "≥ %s"}, {"exclusiveMinimum", "> %s"}, {"maximum", "≤ %s"}, {"exclusiveMaximum", "< %s"},
		{"multipleOf", "multiple of %s"},
		{"minLength", "min length %s"}, {"maxLength", "max length %s"},
		{"minItems", "min items %s"}, {"maxItems", "max items %s"},
		{"minProperties", "min properties %s"}, {"maxProperties", "max properties %s"},
	} {
		// Draft 04-style boolean exclusive bounds aren't numbers; skip them.
		if v, ok := m[k.key]; ok && isNumber(v) {
			fs = append(fs, fmt.Sprintf(k.text, literal(v)))
		}
	}
	if m["uniqueItems"] == true {
		fs = append(fs, "unique items")
	}
	if v, ok := m["const"]; ok {
		fs = append(fs, "always "+literal(v))
	}
	if v, ok := m["default"]; ok {
		fs = append(fs, "default "+literal(v))
	}
	for _, k := range []string{"deprecated", "readOnly", "writeOnly"} {
		if m[k] == true {
			fs = append(fs, k)
		}
	}
	if m["if"] != nil {
		fs = append(fs, "conditional (if/then/else)")
	}
	return fs
}

func isNumber(v any) bool {
	switch v.(type) {
	case float64, int, int64, uint64:
		return true
	}
	return false
}

// literal shows a JSON value: strings as they are, the rest as JSON.
func literal(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
