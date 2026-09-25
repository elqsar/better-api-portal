package compat

import "encoding/json"

// Fingerprint returns a canonical rendering of a schema with its $refs
// inlined, so two schemas can be compared for equality across files. With
// annotations false, descriptions, titles and the like are left out, so a
// documentation-only change doesn't alter the fingerprint.
//
// properties, items and additionalProperties are walked as schemas; other
// keywords are rendered as they are (so an annotation nested under oneOf
// still counts, conservatively). $defs are skipped: whatever is used from
// them is reached through a $ref.
func Fingerprint(s Schema, annotations bool) (string, error) {
	root, err := s.root()
	if err != nil {
		return "", err
	}
	f := &fingerprinter{s: s, annotations: annotations, stack: map[loc]int{}}
	b, err := json.Marshal(f.walk(root))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type fingerprinter struct {
	s           Schema
	annotations bool
	stack       map[loc]int // node → depth, for the nodes being walked
}

func (f *fingerprinter) walk(n node) any {
	m, ok := n.v.(map[string]any)
	if !ok {
		return norm(n.v)
	}
	// A recursive schema is rendered by how far up it points, which is the
	// same for structurally identical schemas in different files.
	if d, ok := f.stack[n.at]; ok {
		return map[string]any{"$cycle": len(f.stack) - d}
	}
	f.stack[n.at] = len(f.stack)
	defer delete(f.stack, n.at)

	out := make(map[string]any, len(m))
	for k, v := range m {
		switch {
		case k == "$defs" || k == "definitions":
		case annotationKeywords[k] && !f.annotations:
		case k == "$ref":
			ref, _ := v.(string)
			target, err := f.s.resolve(n.at, ref)
			if err != nil {
				out[k] = map[string]any{"$unresolved": ref}
			} else {
				out[k] = f.walk(target)
			}
		case k == "properties":
			props, ok := v.(map[string]any)
			if !ok {
				out[k] = norm(v)
				continue
			}
			ps := make(map[string]any, len(props))
			for name, p := range props {
				ps[name] = f.walk(node{p, n.at.child("properties", name)})
			}
			out[k] = ps
		case k == "items" || k == "additionalProperties":
			if _, tuple := v.([]any); tuple {
				out[k] = norm(v)
			} else {
				out[k] = f.walk(node{v, n.at.child(k)})
			}
		default:
			out[k] = norm(v)
		}
	}
	return out
}
