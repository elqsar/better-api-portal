package compat

import (
	"fmt"
	gourl "net/url"
	"path"
	"strconv"
	"strings"

	"better-api-portal/internal/model"
)

// Schema is a payload schema inside its version's bundle.
type Schema struct {
	// Docs maps resource keys to schema documents. A key is a bundle file path
	// ("api/schemas/money.json") or, for inline schemas, a pointer into the
	// file that holds them ("api/events.yaml#/messages/1/dataschema/schema").
	Docs map[string]any
	// Root is the payload's pointer: a resource key, optionally followed by
	// a fragment ("api/a.json#/$defs/E").
	Root string
}

// FromSpec returns the payload schema at pointer payload of a parsed spec.
func FromSpec(s *model.Spec, payload string) Schema {
	docs := make(map[string]any, len(s.Schemas))
	for k, sch := range s.Schemas {
		docs[k] = sch.Doc
	}
	return Schema{Docs: docs, Root: payload}
}

// Node is a schema value and where it lives, for walking a schema outside
// this package, as the web UI's schema tree does.
type Node struct {
	Value any
	at    loc
}

// Key identifies where the node lives, e.g. for cycle detection.
func (n Node) Key() string { return n.at.res + "|" + n.at.frag }

// Top returns the payload's root schema.
func (s Schema) Top() (Node, error) {
	n, err := s.root()
	return Node{n.v, n.at}, err
}

// Follow resolves a $ref found in the node from.
func (s Schema) Follow(from Node, ref string) (Node, error) {
	n, err := s.resolve(from.at, ref)
	return Node{n.v, n.at}, err
}

// Child wraps v, the subschema of n at the keyword path tokens, such as
// ("properties", "total"), so refs inside it resolve from the right place.
func (n Node) Child(v any, tokens ...string) Node {
	return Node{v, n.at.child(tokens...)}
}

// loc is where a schema node lives: a resource and a JSON pointer inside it.
type loc struct {
	res  string
	frag string
}

func (l loc) child(tokens ...string) loc {
	for _, t := range tokens {
		l.frag += "/" + escape(t)
	}
	return l
}

type node struct {
	v  any
	at loc
}

func (s Schema) root() (node, error) {
	if doc, ok := s.Docs[s.Root]; ok {
		return node{doc, loc{res: s.Root}}, nil
	}
	res, frag, _ := strings.Cut(s.Root, "#")
	doc, ok := s.Docs[res]
	if !ok {
		return node{}, fmt.Errorf("schema %s is not in the bundle", s.Root)
	}
	v, err := get(doc, frag)
	if err != nil {
		return node{}, fmt.Errorf("%s: %w", s.Root, err)
	}
	return node{v, loc{res, frag}}, nil
}

// resolve follows ref from a node at from. Only refs into the bundle are
// supported: relative file paths and JSON-pointer fragments.
func (s Schema) resolve(from loc, ref string) (node, error) {
	if u, err := gourl.Parse(ref); err != nil || u.Scheme != "" || u.Host != "" {
		return node{}, fmt.Errorf("$ref %s leaves the bundle", ref)
	}
	file, frag, _ := strings.Cut(ref, "#")
	if frag != "" && !strings.HasPrefix(frag, "/") {
		return node{}, fmt.Errorf("$ref %s uses an anchor", ref)
	}
	res := from.res
	if file != "" {
		base, _, _ := strings.Cut(from.res, "#") // inline schemas resolve next to their file
		res = path.Join(path.Dir(base), file)
	}
	doc, ok := s.Docs[res]
	if !ok {
		return node{}, fmt.Errorf("$ref %s: %s is not in the bundle", ref, res)
	}
	frag, err := gourl.PathUnescape(frag)
	if err != nil {
		return node{}, fmt.Errorf("$ref %s: %w", ref, err)
	}
	v, err := get(doc, frag)
	if err != nil {
		return node{}, fmt.Errorf("$ref %s: %w", ref, err)
	}
	return node{v, loc{res, frag}}, nil
}

// get follows a JSON pointer.
func get(doc any, ptr string) (any, error) {
	if ptr == "" {
		return doc, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", ptr)
	}
	cur := doc
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[tok]
			if !ok {
				return nil, fmt.Errorf("%s not found", ptr)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(c) {
				return nil, fmt.Errorf("%s not found", ptr)
			}
			cur = c[i]
		default:
			return nil, fmt.Errorf("%s not found", ptr)
		}
	}
	return cur, nil
}

func escape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}
