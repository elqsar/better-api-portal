// Package index derives what the portal stores next to a published version
// for browsing and search (docs/spec/05-architecture.md §Storage): the
// model itself, thin rows for queries across APIs, and search documents.
// Everything here can be rebuilt from the stored bundles.
package index

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode"

	"better-api-portal/internal/model"
)

// API is the descriptor metadata indexed with each version.
type API struct {
	ID    string
	Title string // the descriptor's title, if any; else the spec's
	Tags  []string
}

// Version is everything indexed for one published version.
type Version struct {
	Model      *model.Spec
	Messages   []Message
	Operations []Operation
	Bindings   []Binding
	Docs       []Doc
}

// Message is a message type and the API's role for it: who produces and
// who receives an event type, across APIs.
type Message struct {
	Type, Role, Summary string
	Deprecated          bool
}

// Operation is an HTTP operation.
type Operation struct {
	Method, Path, OperationID, Summary string
	Deprecated                         bool
}

// Binding is where a message type is carried, for ce-topic-single-owner.
type Binding struct {
	Type, Role, Protocol, Address string
}

// Search document kinds.
const (
	KindAPI       = "api"
	KindOperation = "operation"
	KindMessage   = "message"
	KindSchema    = "schema"
)

// Doc is a search document. Title is shown and matched by trigram; Terms
// and Body are full-text, with identifiers split into words, so "refund"
// finds com.acme.orders.refund.issued.v1 and POST /orders/{id}/refunds.
type Doc struct {
	Kind  string
	Ref   string // what the result links to: the API id, "METHOD path", the message type or the schema pointer
	Title string
	Terms string // weighted above Body
	Body  string
}

// maxBody bounds a document's body; schemas can be large.
const maxBody = 32 << 10

// Build indexes a version of the API.
func Build(api API, spec *model.Spec) *Version {
	v := &Version{Model: spec}
	title := api.Title
	if title == "" {
		title = spec.Title
	}
	v.Docs = append(v.Docs, Doc{Kind: KindAPI, Ref: api.ID, Title: title,
		Terms: Words(title + " " + api.ID),
		Body:  limit(spec.Description + " " + strings.Join(api.Tags, " ") + " " + Words(strings.Join(api.Tags, " ")))})

	for _, m := range spec.Messages {
		v.Messages = append(v.Messages, Message{Type: m.Key, Role: string(m.Role), Summary: m.Summary, Deprecated: m.Deprecated})
		var addrs []string
		for _, b := range m.Bindings {
			v.Bindings = append(v.Bindings, Binding{Type: m.Key, Role: string(m.Role), Protocol: b.Protocol, Address: b.Address})
			addrs = append(addrs, b.Protocol+" "+b.Address+" "+Words(b.Address))
		}
		v.Docs = append(v.Docs, Doc{Kind: KindMessage, Ref: m.Key, Title: m.Key, Terms: Words(m.Key),
			Body: limit(strings.Join(append([]string{m.Summary, m.Description, string(m.Role)}, addrs...), " "))})
	}
	for _, o := range spec.Operations {
		v.Operations = append(v.Operations, Operation{Method: o.Method, Path: o.Path, OperationID: o.OperationID,
			Summary: o.Summary, Deprecated: o.Deprecated})
		ref := o.Method + " " + o.Path
		v.Docs = append(v.Docs, Doc{Kind: KindOperation, Ref: ref, Title: ref,
			Terms: Words(o.Path + " " + o.OperationID),
			Body:  limit(strings.Join(append([]string{o.Summary, o.Description, Words(strings.Join(o.Tags, " "))}, o.Tags...), " "))})
	}
	for _, ptr := range slices.Sorted(maps.Keys(spec.Schemas)) {
		s := spec.Schemas[ptr]
		name := SchemaName(ptr)
		var b strings.Builder
		schemaText(&b, s.Doc, 0)
		v.Docs = append(v.Docs, Doc{Kind: KindSchema, Ref: ptr, Title: name, Terms: Words(name), Body: limit(b.String())})
	}
	return v
}

// SchemaName is a schema's display name: the component name for
// "file#/components/schemas/Refund", the last pointer token for another
// fragment, else the file name without extensions ("order-created.v1").
func SchemaName(ptr string) string {
	file, frag, ok := strings.Cut(ptr, "#")
	if ok && frag != "" && frag != "/" {
		tok := frag[strings.LastIndexByte(frag, '/')+1:]
		return strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
	}
	base := path.Base(file)
	for _, ext := range []string{".json", ".yaml", ".yml"} {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// schemaText collects the words a schema mentions: property names,
// titles, descriptions and enum values.
func schemaText(b *strings.Builder, doc any, depth int) {
	if depth > 32 || b.Len() > maxBody {
		return
	}
	switch d := doc.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(d)) {
			switch k {
			case "title", "description":
				if s, ok := d[k].(string); ok {
					fmt.Fprintf(b, "%s ", s)
				}
				continue
			case "enum":
				if vs, ok := d[k].([]any); ok {
					for _, v := range vs {
						if s, ok := v.(string); ok {
							fmt.Fprintf(b, "%s %s ", s, Words(s))
						}
					}
				}
				continue
			case "properties", "patternProperties":
				if ps, ok := d[k].(map[string]any); ok {
					for _, name := range slices.Sorted(maps.Keys(ps)) {
						fmt.Fprintf(b, "%s %s ", name, Words(name))
						schemaText(b, ps[name], depth+1)
					}
				}
				continue
			case "examples", "example", "default", "const":
				continue // data, not vocabulary
			}
			schemaText(b, d[k], depth+1)
		}
	case []any:
		for _, x := range d {
			schemaText(b, x, depth+1)
		}
	}
}

// Words splits identifiers into lower-case words: dots, slashes, braces,
// dashes and underscores separate, and so do camelCase humps.
// "com.acme.orders.refund.issued.v1" → "com acme orders refund issued v1";
// "/orders/{orderId}/refunds" → "orders order id refunds".
func Words(s string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// A hump: lower→Upper, or the last capital of an acronym
			// before a lower-case letter (HTTPServer → http server).
			if unicode.IsUpper(r) && len(cur) > 0 {
				prev := rs[i-1]
				next := rune(0)
				if i+1 < len(rs) {
					next = rs[i+1]
				}
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && unicode.IsLower(next)) {
					flush()
				}
			}
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return strings.Join(words, " ")
}

func limit(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxBody {
		return s
	}
	cut := maxBody
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
