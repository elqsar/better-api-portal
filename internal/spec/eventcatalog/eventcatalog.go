// Package eventcatalog parses events.yaml into the normalised model
// (docs/spec/03-formats.md §2).
package eventcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"better-api-portal/docs/spec/schemas"
	"better-api-portal/internal/bundle"
	"better-api-portal/internal/model"
	"better-api-portal/internal/yamldoc"
)

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return yamldoc.CompileEmbedded(schemas.FS, "eventcatalog.schema.json", "https://api-portal.dev/schemas/eventcatalog/1.0.json")
})

// Result is a parsed event catalogue.
type Result struct {
	Spec *model.Spec
	// Payloads holds the compiled payload schema of each message, by type,
	// for rules that validate data against it.
	Payloads map[string]*jsonschema.Schema
	// Doc is the parsed entry file, for resolving pointers to lines.
	Doc *yamldoc.Doc
}

// Parse reads the event catalogue at specPath. root is the descriptor's
// directory: $refs may not leave it, and paths in the model are relative to
// it. Problems with the catalogue are findings; the error is reserved for
// failures to run at all. The result is nil when the catalogue doesn't match
// its schema.
func Parse(root, specPath string) (*Result, []model.Finding, error) {
	b, err := os.ReadFile(specPath)
	if err != nil {
		return nil, nil, err
	}
	doc, err := yamldoc.Parse(specPath, b)
	var se *yamldoc.SyntaxError
	if errors.As(err, &se) {
		return nil, []model.Finding{{
			RuleID:   "eventcatalog-syntax",
			Severity: model.SeverityError,
			Message:  "not valid YAML: " + se.Error(),
			File:     specPath,
			Line:     se.Line,
		}}, nil
	}
	if err != nil {
		return nil, nil, err
	}

	sch, err := compiled()
	if err != nil {
		return nil, nil, err
	}
	data := doc.JSON()
	violations, err := yamldoc.Validate(sch, data, yamldoc.Message)
	if err != nil {
		return nil, nil, err
	}
	if len(violations) > 0 {
		findings := make([]model.Finding, len(violations))
		for i, v := range violations {
			findings[i] = model.Finding{
				RuleID:   "eventcatalog-schema",
				Severity: model.SeverityError,
				Message:  v.Message,
				File:     specPath,
				Pointer:  v.Pointer,
				Line:     doc.Line(v.Pointer),
			}
		}
		return nil, findings, nil
	}

	var cat catalogue
	if err := decode(data, &cat); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", specPath, err)
	}
	p, err := newParser(root, specPath, doc)
	if err != nil {
		return nil, nil, err
	}
	return p.build(&cat)
}

// catalogue mirrors events.yaml; the schema has already validated its shape.
type catalogue struct {
	Title       string    `json:"title"`
	Version     string    `json:"version"`
	Description string    `json:"description"`
	Defaults    defaults  `json:"defaults"`
	Messages    []message `json:"messages"`
}

type defaults struct {
	Source          string    `json:"source"`
	DataContentType string    `json:"datacontenttype"`
	Bindings        []binding `json:"bindings"`
}

type message struct {
	Type            string                     `json:"type"`
	Role            model.Role                 `json:"role"`
	Summary         string                     `json:"summary"`
	Description     string                     `json:"description"`
	Source          string                     `json:"source"`
	Subject         string                     `json:"subject"`
	DataContentType string                     `json:"datacontenttype"`
	DataSchema      dataschema                 `json:"dataschema"`
	DataSchemaURI   string                     `json:"dataschemauri"`
	Extensions      map[string]model.Extension `json:"extensions"`
	Bindings        []binding                  `json:"bindings"`
	Examples        []model.Example            `json:"examples"`
	Deprecated      bool                       `json:"deprecated"`
}

type dataschema struct {
	Ref    string         `json:"$ref"`
	Schema map[string]any `json:"schema"`
}

// binding has exactly one key, the protocol.
type binding map[string]map[string]any

// decode converts the JSON data model into v, keeping numbers exact.
func decode(data, v any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

type parser struct {
	root     string // absolute
	specPath string
	specDir  string // absolute
	doc      *yamldoc.Doc
	loader   *loader
	compiler *jsonschema.Compiler
	findings []model.Finding
}

func newParser(root, specPath string, doc *yamldoc.Doc) (*parser, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absSpec, err := filepath.Abs(specPath)
	if err != nil {
		return nil, err
	}
	l := newLoader(absRoot)
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(l)
	return &parser{
		root:     absRoot,
		specPath: specPath,
		specDir:  filepath.Dir(absSpec),
		doc:      doc,
		loader:   l,
		compiler: c,
	}, nil
}

func (p *parser) build(cat *catalogue) (*Result, []model.Finding, error) {
	entry, err := p.rel(filepath.Join(p.specDir, filepath.Base(p.specPath)))
	if err != nil {
		return nil, nil, err
	}
	spec := &model.Spec{
		Kind:        "cloudevents",
		Title:       cat.Title,
		Version:     cat.Version,
		Description: cat.Description,
		Schemas:     map[string]*model.Schema{},
	}
	res := &Result{Spec: spec, Payloads: map[string]*jsonschema.Schema{}, Doc: p.doc}

	firstByType := map[string]int{}
	for i, m := range cat.Messages {
		base := "/messages/" + strconv.Itoa(i)
		if first, dup := firstByType[m.Type]; dup {
			p.addFinding("eventcatalog-duplicate-type", base+"/type",
				fmt.Sprintf("type %s is already declared by messages[%d]", m.Type, first))
			continue
		}
		firstByType[m.Type] = i

		msg := model.Message{
			Key:         m.Type,
			Role:        m.Role,
			Summary:     m.Summary,
			Description: m.Description,
			CE: model.CEAttributes{
				Source:          firstNonEmpty(m.Source, cat.Defaults.Source),
				Subject:         m.Subject,
				DataContentType: firstNonEmpty(m.DataContentType, cat.Defaults.DataContentType, "application/json"),
				DataSchemaURI:   m.DataSchemaURI,
				Extensions:      m.Extensions,
			},
			Examples:   m.Examples,
			Deprecated: m.Deprecated,
			Pointer:    base,
			Line:       p.doc.Line(base),
		}
		// A message's own bindings replace the defaults; they aren't merged.
		if m.Bindings != nil {
			msg.Bindings = normaliseBindings(m.Bindings, base+"/bindings")
		} else {
			msg.Bindings = normaliseBindings(cat.Defaults.Bindings, "/defaults/bindings")
		}

		schema, payload, err := p.resolve(i, entry, m.DataSchema)
		if err != nil {
			p.addFinding("ce-dataschema-resolves", base+"/dataschema", err.Error())
		} else {
			msg.Payload = payload
			res.Payloads[m.Type] = schema
		}
		spec.Messages = append(spec.Messages, msg)
	}

	// Record every schema document the compiler loaded, including nested $refs.
	files := []string{entry}
	for ptr, draft := range p.loader.drafts {
		spec.Schemas[ptr] = &model.Schema{Pointer: ptr, Draft: draft, Doc: p.loader.docs[ptr]}
		if !strings.Contains(ptr, "#") { // inline schemas live in the entry file
			files = append(files, ptr)
		}
	}
	slices.Sort(files[1:])
	spec.Files = files
	return res, p.findings, nil
}

// resolve compiles a message's dataschema, returning it and its pointer.
func (p *parser) resolve(i int, entry string, ds dataschema) (*jsonschema.Schema, string, error) {
	if ds.Schema != nil {
		// Inline schemas get a URL next to the catalogue, so that their
		// relative $refs resolve against its directory.
		url := fileURL(filepath.Join(p.specDir, fmt.Sprintf("%s.inline-%d.json", filepath.Base(p.specPath), i)))
		ptr := entry + "#" + yamldoc.Pointer("messages", strconv.Itoa(i), "dataschema", "schema")
		p.loader.names[url] = ptr
		draft, err := checkDraft(ds.Schema)
		if err != nil {
			return nil, "", err
		}
		if err := p.compiler.AddResource(url, ds.Schema); err != nil {
			return nil, "", err
		}
		s, err := p.compiler.Compile(url)
		if err != nil {
			return nil, "", p.loader.compileError(err)
		}
		p.loader.inline(ptr, draft, ds.Schema)
		return s, ptr, nil
	}

	ref, frag, _ := cutFragment(ds.Ref)
	if isRemote(ref) {
		return nil, "", fmt.Errorf("remote $ref %s is not supported; reference a file in the repo", ds.Ref)
	}
	abs := filepath.Join(p.specDir, filepath.FromSlash(ref))
	rel, err := p.rel(abs)
	if err != nil {
		return nil, "", fmt.Errorf("$ref %s leaves the descriptor's directory", ds.Ref)
	}
	url := fileURL(abs)
	if frag != "" {
		url += "#" + frag
	}
	s, err := p.compiler.Compile(url)
	if err != nil {
		return nil, "", p.loader.compileError(err)
	}
	if frag != "" {
		rel += "#" + frag
	}
	return s, rel, nil
}

// rel returns abs as a slash path relative to the root, or an error if it
// lies outside it.
func (p *parser) rel(abs string) (string, error) {
	return bundle.RelTo(p.root, abs)
}

func (p *parser) addFinding(rule, ptr, msg string) {
	p.findings = append(p.findings, model.Finding{
		RuleID:   rule,
		Severity: model.SeverityError,
		Message:  msg,
		File:     p.specPath,
		Pointer:  ptr,
		Line:     p.doc.Line(ptr),
	})
}

// normaliseBindings maps each {protocol: {...}} item to a model.Binding,
// moving the protocol's address field into Address.
// base is the pointer of the list in the source file.
func normaliseBindings(bs []binding, base string) []model.Binding {
	out := make([]model.Binding, 0, len(bs))
	for i, b := range bs {
		for proto, fields := range b {
			props := make(map[string]any, len(fields))
			for k, v := range fields {
				props[k] = v
			}
			var addr string
			for _, k := range addressKeys[proto] {
				if v, ok := props[k].(string); ok {
					addr = v
					delete(props, k)
					break
				}
			}
			if proto == "kafka" && props["mode"] == nil {
				props["mode"] = "binary"
			}
			if len(props) == 0 {
				props = nil
			}
			out = append(out, model.Binding{
				Protocol: proto,
				Address:  addr,
				Props:    props,
				Pointer:  base + "/" + strconv.Itoa(i),
			})
		}
	}
	return out
}

// addressKeys lists, per protocol, the field that names where messages go.
var addressKeys = map[string][]string{
	"kafka":       {"topic"},
	"nats":        {"subject"},
	"sns":         {"topic"},
	"sqs":         {"queue"},
	"eventbridge": {"bus"},
	"pubsub":      {"topic"},
	"servicebus":  {"topic", "queue"},
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
