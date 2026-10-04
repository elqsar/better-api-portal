package agentdoc

import (
	"path"
	"strings"

	"github.com/elqsar/better-api-portal/internal/bundle"
	"github.com/elqsar/better-api-portal/internal/check"
	"github.com/elqsar/better-api-portal/internal/compat"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// Version is one version of an API, parsed, with every document of its
// bundle so schemas and raw OpenAPI objects resolve.
type Version struct {
	Spec *model.Spec
	// Docs maps a resource (a bundle file, or an inline schema's pointer)
	// to its JSON value, as compat.Schema expects.
	Docs map[string]any
}

// Load parses a bundle into a Version.
func Load(b *bundle.Bundle) (*Version, error) {
	spec, err := check.ParseBundle(b)
	if err != nil {
		return nil, err
	}
	v := &Version{Spec: spec, Docs: map[string]any{}}
	for name, data := range b.Files {
		d, err := yamldoc.Parse(name, data)
		if err != nil {
			return nil, err
		}
		v.Docs[name] = d.JSON()
	}
	// OpenAPI keys its component schemas by pointer into the entry file,
	// which the entry file's own document already covers; resolving through
	// it lets their "#/components/..." refs work. Event catalogues key inline
	// payload schemas by pointer too, and those stay.
	for k, s := range spec.Schemas {
		if _, ok := v.Docs[k]; ok || (spec.Kind == "openapi" && strings.Contains(k, "#")) {
			continue
		}
		v.Docs[k] = s.Doc
	}
	return v, nil
}

// entry is the spec's entry file.
func (v *Version) entry() string {
	if len(v.Spec.Files) == 0 {
		return ""
	}
	return v.Spec.Files[0]
}

// schema returns a resolver rooted at pointer root.
func (v *Version) schema(root string) compat.Schema {
	return compat.Schema{Docs: v.Docs, Root: root}
}

// raw returns the value at pointer ptr of the entry file, following $refs,
// or nil if there's none.
func (v *Version) raw(ptr string) map[string]any {
	s := v.schema(v.entry() + "#" + ptr)
	n, err := s.Top()
	if err != nil {
		return nil
	}
	n = v.deref(s, n)
	m, _ := n.Value.(map[string]any)
	return m
}

// deref follows the $refs of n, a few hops at most.
func (v *Version) deref(s compat.Schema, n compat.Node) compat.Node {
	for range 8 {
		m, ok := n.Value.(map[string]any)
		if !ok {
			return n
		}
		ref, ok := m["$ref"].(string)
		if !ok {
			return n
		}
		next, err := s.Follow(n, ref)
		if err != nil {
			return n
		}
		n = next
	}
	return n
}

// refName is a short name for what a $ref points to: the last pointer
// token, or the file name without its extension.
func refName(ref string) string {
	file, frag, _ := strings.Cut(ref, "#")
	if frag != "" && frag != "/" {
		return frag[strings.LastIndex(frag, "/")+1:]
	}
	base := path.Base(file)
	return strings.TrimSuffix(base, path.Ext(base))
}
