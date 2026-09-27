// Package openapi parses OpenAPI 3.0 and 3.1 documents into the normalised
// model (docs/spec/02-domain-model.md). Structural validation against the
// OpenAPI schema is left to the lint step (vacuum's oas3-schema rule); the
// parser only needs the parts the model is built from, and tolerates the
// rest being malformed.
package openapi

import (
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/elqsar/better-api-portal/internal/bundle"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// Result is a parsed OpenAPI document.
type Result struct {
	Spec *model.Spec
	// Doc and Raw are the entry file, parsed, for rules that need lines.
	Doc *yamldoc.Doc
	Raw map[string]any
	// Bytes is the entry file as read, for linters that parse it themselves.
	Bytes []byte
	// Closure holds every file of the spec.
	Closure *bundle.Closure
}

// Parse reads the OpenAPI document at specPath. root is the descriptor's
// directory: $refs may not leave it, and paths in the model are relative to
// it. Problems are findings; the error is reserved for failures to run at
// all. The result is nil when the document can't be read as a whole.
func Parse(root, specPath string) (*Result, []model.Finding, error) {
	b, err := os.ReadFile(specPath)
	if err != nil {
		return nil, nil, err
	}
	c, problems, err := bundle.Load(root, specPath)
	var se *yamldoc.SyntaxError
	if errors.As(err, &se) {
		return nil, []model.Finding{{
			RuleID:   "openapi-syntax",
			Severity: model.SeverityError,
			Message:  "not valid YAML or JSON: " + se.Error(),
			File:     specPath,
			Line:     se.Line,
		}}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var findings []model.Finding
	for _, p := range problems {
		findings = append(findings, model.Finding{RuleID: "openapi-ref", Severity: model.SeverityError,
			Message: p.Message, File: p.File, Pointer: p.Pointer, Line: p.Line})
	}
	if len(findings) > 0 {
		return nil, findings, nil
	}

	doc := c.Docs[c.Entry]
	raw, _ := doc.JSON().(map[string]any)
	if raw == nil {
		return nil, []model.Finding{{RuleID: "openapi-invalid", Severity: model.SeverityError,
			Message: "the document is not an object", File: specPath, Line: 1}}, nil
	}
	p := &parser{c: c, entry: c.Entry}
	spec := p.build(raw)

	if !semver.IsValid("v" + spec.Version) {
		findings = append(findings, model.Finding{RuleID: "openapi-version", Severity: model.SeverityError,
			Message: fmt.Sprintf("info.version %q is not a semantic version such as 1.2.0", spec.Version),
			File:    specPath, Pointer: "/info/version", Line: doc.Line("/info/version")})
	}
	return &Result{Spec: spec, Doc: doc, Raw: raw, Bytes: b, Closure: c}, findings, nil
}

type parser struct {
	c     *bundle.Closure
	entry string
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func (p *parser) build(raw map[string]any) *model.Spec {
	info := obj(raw["info"])
	spec := &model.Spec{
		Kind:        "openapi",
		Title:       str(info["title"]),
		Version:     str(info["version"]),
		Description: str(info["description"]),
		Schemas:     map[string]*model.Schema{},
		Files:       p.c.Files(),
	}
	for _, s := range list(raw["servers"]) {
		if u := str(obj(s)["url"]); u != "" {
			spec.Servers = append(spec.Servers, u)
		}
	}

	draft := "2020-12"
	if strings.HasPrefix(str(raw["openapi"]), "3.0") {
		draft = "oas3.0"
	}
	for name, s := range obj(obj(raw["components"])["schemas"]) {
		key := p.entry + "#" + yamldoc.Pointer("components", "schemas", name)
		spec.Schemas[key] = &model.Schema{Pointer: key, Draft: draft, Doc: s}
	}
	for _, f := range spec.Files[1:] {
		spec.Schemas[f] = &model.Schema{Pointer: f, Draft: draft, Doc: p.c.Docs[f].JSON()}
	}

	globalSecurity, _ := raw["security"].([]any)
	paths := obj(raw["paths"])
	for _, pth := range sortedKeys(paths) {
		item, itemAt := p.deref(paths[pth], p.entry, yamldoc.Pointer("paths", pth))
		pathParams := list(item["parameters"])
		for _, m := range methods {
			op := obj(item[m])
			if op == nil {
				continue
			}
			ptr := yamldoc.Pointer("paths", pth, m)
			o := model.Operation{
				Method:      strings.ToUpper(m),
				Path:        pth,
				OperationID: str(op["operationId"]),
				Summary:     str(op["summary"]),
				Description: str(op["description"]),
				Tags:        stringList(op["tags"]),
				Deprecated:  op["deprecated"] == true,
				Pointer:     ptr,
				Line:        p.c.Docs[p.entry].Line(ptr),
			}
			security := globalSecurity
			if s, ok := op["security"].([]any); ok {
				security = s
			}
			o.Security = schemeNames(security)
			o.Parameters = p.parameters(pathParams, itemAt, list(op["parameters"]), itemAt.child(m, "parameters"))
			if body, at := p.deref(op["requestBody"], itemAt.file, itemAt.child(m, "requestBody").ptr); body != nil {
				o.Request = p.content(body["content"], at.child("content"))
			}
			responses := obj(op["responses"])
			if len(responses) > 0 {
				o.Responses = map[string]map[string]string{}
				for _, status := range sortedKeys(responses) {
					resp, at := p.deref(responses[status], itemAt.file, itemAt.child(m, "responses", status).ptr)
					o.Responses[status] = p.content(resp["content"], at.child("content"))
				}
			}
			spec.Operations = append(spec.Operations, o)
		}
	}
	return spec
}

// at locates a node: a file of the closure and a pointer in it.
type at struct {
	file, ptr string
}

func (a at) child(tokens ...string) at {
	a.ptr += yamldoc.Pointer(tokens...)
	return a
}

func (a at) key() string {
	if a.ptr == "" {
		return a.file
	}
	return a.file + "#" + a.ptr
}

// deref follows $refs from v, found at ptr in file, and returns the object
// reached and where it is. Refs that can't be followed give nil.
func (p *parser) deref(v any, file, ptr string) (map[string]any, at) {
	loc := at{file, ptr}
	for range 32 {
		m := obj(v)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m, loc
		}
		target, frag, _ := strings.Cut(ref, "#")
		if target != "" {
			loc.file = path.Join(path.Dir(loc.file), target)
		}
		d, ok := p.c.Docs[loc.file]
		if !ok {
			return nil, loc
		}
		loc.ptr = frag
		v = get(d.JSON(), frag)
	}
	return nil, loc
}

// parameters merges path-level and operation-level parameters; the latter
// override the former by name and location.
func (p *parser) parameters(pathParams []any, pathAt at, opParams []any, opAt at) []model.Parameter {
	var out []model.Parameter
	index := map[string]int{}
	add := func(params []any, base at) {
		for i, raw := range params {
			prm, loc := p.deref(raw, base.file, base.ptr+fmt.Sprintf("/%d", i))
			if prm == nil {
				continue
			}
			mp := model.Parameter{Name: str(prm["name"]), In: str(prm["in"]), Required: prm["required"] == true}
			if s, ok := prm["schema"]; ok {
				mp.Schema = p.schemaPointer(s, loc.child("schema"))
			}
			k := mp.In + " " + mp.Name
			if j, ok := index[k]; ok {
				out[j] = mp
				continue
			}
			index[k] = len(out)
			out = append(out, mp)
		}
	}
	add(pathParams, pathAt.child("parameters"))
	add(opParams, opAt)
	return out
}

// content maps media types to schema pointers.
func (p *parser) content(v any, loc at) map[string]string {
	out := map[string]string{}
	c := obj(v)
	for _, mt := range sortedKeys(c) {
		if s, ok := obj(c[mt])["schema"]; ok {
			out[mt] = p.schemaPointer(s, loc.child(mt, "schema"))
		} else {
			out[mt] = ""
		}
	}
	return out
}

// schemaPointer names a schema by where its definition is: the target of
// its $ref, or its own location when inline.
func (p *parser) schemaPointer(v any, loc at) string {
	if ref, ok := obj(v)["$ref"].(string); ok {
		target, frag, _ := strings.Cut(ref, "#")
		file := loc.file
		if target != "" {
			file = path.Join(path.Dir(file), target)
		}
		return at{file, frag}.key()
	}
	return loc.key()
}

func schemeNames(reqs []any) []string {
	seen := map[string]bool{}
	for _, r := range reqs {
		for name := range obj(r) {
			seen[name] = true
		}
	}
	return sortedKeys(seen)
}

func get(doc any, ptr string) any {
	if ptr == "" {
		return doc
	}
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch c := cur.(type) {
		case map[string]any:
			cur = c[tok]
		case []any:
			var i int
			if _, err := fmt.Sscanf(tok, "%d", &i); err != nil || i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }

func stringList(v any) []string {
	var out []string
	for _, x := range list(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
