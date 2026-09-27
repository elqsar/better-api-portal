package compat

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
)

// FieldChange is one field-level difference between two payload schemas,
// for describing a change Check found compatible.
type FieldChange struct {
	// Path is the instance location, as in Issue.Path.
	Path string
	// What happened: "added", "removed", "now required", "now optional",
	// "enum value added" or "enum value removed".
	What string
	// Detail qualifies it: "optional" or "required" for an added field, the
	// value for an enum change.
	Detail string
}

// maxFieldDepth bounds the walk, as the UI's schema tree does.
const maxFieldDepth = 16

// FieldChanges lists the properties added and removed, required changes
// and enum values added and removed between two schemas, following
// properties and array items through $refs. Other keywords (constraints,
// combinators) aren't described; an empty result means only those changed.
func FieldChanges(old, new Schema) ([]FieldChange, error) {
	o, err := old.Top()
	if err != nil {
		return nil, err
	}
	n, err := new.Top()
	if err != nil {
		return nil, err
	}
	w := fieldWalk{old: old, new: new, seen: map[string]bool{}}
	w.walk(o, n, "", 0)
	sort.SliceStable(w.out, func(i, j int) bool { return w.out[i].Path < w.out[j].Path })
	return w.out, nil
}

type fieldWalk struct {
	old, new Schema
	seen     map[string]bool // old|new node pairs already compared
	out      []FieldChange
}

func (w *fieldWalk) walk(o, n Node, path string, depth int) {
	o, n = deref(w.old, o), deref(w.new, n)
	om, ok1 := o.Value.(map[string]any)
	nm, ok2 := n.Value.(map[string]any)
	if !ok1 || !ok2 || depth > maxFieldDepth {
		return
	}
	pair := o.Key() + "||" + n.Key()
	if w.seen[pair] {
		return // recursive schema
	}
	w.seen[pair] = true
	defer delete(w.seen, pair)

	add := func(p, what, detail string) { w.out = append(w.out, FieldChange{p, what, detail}) }

	oldEnum, newEnum := enumValues(om), enumValues(nm)
	if oldEnum != nil && newEnum != nil {
		for _, v := range newEnum {
			if !slices.Contains(oldEnum, v) {
				add(path, "enum value added", v)
			}
		}
		for _, v := range oldEnum {
			if !slices.Contains(newEnum, v) {
				add(path, "enum value removed", v)
			}
		}
	}

	oldProps, _ := om["properties"].(map[string]any)
	newProps, _ := nm["properties"].(map[string]any)
	oldReq, newReq := required(om), required(nm)
	names := slices.Sorted(maps.Keys(newProps))
	for name := range oldProps {
		if _, ok := newProps[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		p := path + "/" + name
		ov, inOld := oldProps[name]
		nv, inNew := newProps[name]
		switch {
		case !inOld:
			detail := "optional"
			if newReq[name] {
				detail = "required"
			}
			add(p, "added", detail)
		case !inNew:
			add(p, "removed", "")
		default:
			if newReq[name] && !oldReq[name] {
				add(p, "now required", "")
			} else if oldReq[name] && !newReq[name] {
				add(p, "now optional", "")
			}
			w.walk(o.Child(ov, "properties", name), n.Child(nv, "properties", name), p, depth+1)
		}
	}

	if oi, ok := om["items"].(map[string]any); ok {
		if ni, ok := nm["items"].(map[string]any); ok {
			w.walk(o.Child(oi, "items"), n.Child(ni, "items"), path+"/[]", depth+1)
		}
	}
}

// deref follows $refs, stopping at one it can't resolve or at a cycle.
func deref(s Schema, n Node) Node {
	for range maxFieldDepth {
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

func required(m map[string]any) map[string]bool {
	out := map[string]bool{}
	list, _ := m["required"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}

// enumValues are the schema's enum values as compact JSON, nil if it has
// no enum.
func enumValues(m map[string]any) []string {
	list, ok := m["enum"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}
	return out
}
