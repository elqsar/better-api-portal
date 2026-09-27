package initkit

import (
	"bytes"
	"cmp"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// EventsOptions describe an event catalogue drafted from JSON Schemas.
type EventsOptions struct {
	// Prefix starts every type named from a file, e.g. "com.acme.orders.".
	Prefix string
	Title  string
	// KafkaTopic or NATSSubject become the catalogue's default binding.
	KafkaTopic, NATSSubject string
}

// EventType is a message of the draft and where its type came from.
type EventType struct {
	Type   string
	Schema string // relative to the catalogue, "./schemas/x.v1.json"
	// FromID: the type is the schema's $id, an existing name kept as is.
	// Otherwise it is named from the file.
	FromID bool
	// Problems say why the type fails the portal's rules (ce-type-format,
	// the prefix); empty if it passes.
	Problems []string
}

// typeRe is lint's ce-type-format.
var typeRe = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+\.v([0-9]+)$`)

// majorSuffix is a major version at the end of a file name or $id, as in
// lint's ce-type-major-matches-schema.
var majorSuffix = regexp.MustCompile(`(?:^|[./_-])v([0-9]+)$`)

// Events drafts an event catalogue at out from the JSON Schemas under dir:
// one produced message per schema that no other schema there refers to
// (those are shared parts, like a money type). A schema's $id is kept as the
// type when it has no URL scheme and is at least two words; otherwise the type is
// Prefix + the file name split into words + .v<major> (major from the file
// name or $id, else 1). It returns the catalogue and its types.
func Events(dir, out string, opts EventsOptions) ([]byte, []EventType, error) {
	type schemaFile struct {
		rel string // relative to dir
		doc map[string]any
	}
	var files []schemaFile
	referenced := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var doc map[string]any
		if yaml.Unmarshal(b, &doc) != nil || !isSchema(doc) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		files = append(files, schemaFile{rel, doc})
		for _, ref := range refs(doc) {
			file, _, _ := strings.Cut(ref, "#")
			if file != "" && !strings.Contains(file, "://") {
				referenced[path.Join(path.Dir(rel), file)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	relDir, err := filepath.Rel(filepath.Dir(out), dir)
	if err != nil {
		return nil, nil, err
	}
	prefix := opts.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}
	var types []EventType
	var summaries []string
	for _, f := range files {
		if referenced[f.rel] {
			continue
		}
		t := EventType{Schema: "./" + path.Join(filepath.ToSlash(relDir), f.rel)}
		id, _ := f.doc["$id"].(string)
		id = strings.TrimSuffix(id, "#")
		if id != "" && !strings.Contains(id, "://") && strings.Contains(id, ".") {
			t.Type, t.FromID = id, true
		} else {
			major := "1"
			name := strings.TrimSuffix(path.Base(f.rel), path.Ext(f.rel))
			if m := majorSuffix.FindStringSubmatch(name); m != nil {
				major, name = m[1], name[:len(name)-len(m[0])]
			} else if m := majorSuffix.FindStringSubmatch(id); m != nil {
				major = m[1]
			}
			t.Type = prefix + strings.Join(words(name), ".") + ".v" + major
		}
		if !typeRe.MatchString(t.Type) {
			t.Problems = append(t.Problems, "not reverse-DNS, lower-case and ending in .vN (ce-type-format)")
		}
		if prefix != "" && !strings.HasPrefix(t.Type, prefix) {
			t.Problems = append(t.Problems, "doesn't start with "+prefix+" (ce-type-prefix)")
		}
		types = append(types, t)
		summary := "TODO: one line on when it is emitted"
		if d, _ := f.doc["description"].(string); strings.TrimSpace(d) != "" {
			summary = strings.TrimSpace(strings.SplitN(strings.TrimSpace(d), "\n", 2)[0])
		}
		summaries = append(summaries, summary)
	}
	if len(types) == 0 {
		return nil, nil, fmt.Errorf("no JSON Schema under %s", dir)
	}

	var b bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# An event catalogue drafted by portal init from the JSON Schemas in %s.\n", filepath.ToSlash(relDir))
	w("# Format: https://github.com/elqsar/better-api-portal/blob/main/docs/spec/03-formats.md#2-event-catalogue--eventsyaml\n")
	w("eventcatalog: \"1.0\"\n")
	w("title: %s\n", yamlString(cmp.Or(opts.Title, "TODO")))
	w("version: 1.0.0 # the contract's semver; bump it when the contract changes\n")
	w("description: TODO what these events are for.\n\n")
	w("defaults:\n  datacontenttype: application/json\n")
	switch {
	case opts.KafkaTopic != "":
		w("  bindings:\n    - kafka: { topic: %s, mode: binary }\n", yamlString(opts.KafkaTopic))
	case opts.NATSSubject != "":
		w("  bindings:\n    - nats: { subject: %s }\n", yamlString(opts.NATSSubject))
	default:
		w("  # TODO where the events are carried; every message needs a binding, e.g.\n")
		w("  # bindings:\n  #   - kafka: { topic: orders.events, key: { from: data, pointer: /orderId }, mode: binary }\n")
	}
	w("\nmessages:\n")
	for i, t := range types {
		if i > 0 {
			w("\n")
		}
		w("  - type: %s\n", t.Type)
		w("    role: produces # receives if other services send it to you and you own the contract;\n")
		w("                   # another team's events go in portal.yaml's consumes instead\n")
		w("    summary: %s\n", yamlString(summaries[i]))
		w("    dataschema: { $ref: %s }\n", yamlString(t.Schema))
	}
	return b.Bytes(), types, nil
}

// isSchema tells a JSON Schema from other YAML or JSON: it has $schema, or
// a type or properties, and isn't a spec.
func isSchema(doc map[string]any) bool {
	for _, k := range []string{"openapi", "asyncapi", "eventcatalog", "swagger", "apiVersion"} {
		if _, ok := doc[k]; ok {
			return false
		}
	}
	for _, k := range []string{"$schema", "type", "properties"} {
		if _, ok := doc[k]; ok {
			return true
		}
	}
	return false
}

// refs lists every $ref in a document.
func refs(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		if r, ok := v["$ref"].(string); ok {
			out = append(out, r)
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			out = append(out, refs(v[k])...)
		}
	case []any:
		for _, x := range v {
			out = append(out, refs(x)...)
		}
	}
	return out
}

// words splits a file name into lower-case words at separators and
// camelCase boundaries: "orderCreated", "order-created" → order, created.
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}
