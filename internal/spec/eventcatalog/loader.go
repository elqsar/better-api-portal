package eventcatalog

import (
	"errors"
	"fmt"
	gourl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"better-api-portal/internal/bundle"
	"better-api-portal/internal/yamldoc"
)

// loader serves schema documents to the compiler from inside the root only,
// and records every document it serves: together they are the bundle closure.
type loader struct {
	root   string
	drafts map[string]string // rel path (or inline pointer) → draft
	docs   map[string]any
	names  map[string]string // synthetic URL of an inline schema → its pointer
}

func newLoader(root string) *loader {
	return &loader{root: root, drafts: map[string]string{}, docs: map[string]any{}, names: map[string]string{}}
}

func (l *loader) Load(url string) (any, error) {
	u, err := gourl.Parse(url)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "file" {
		return nil, fmt.Errorf("remote $ref %s is not supported; reference a file in the repo", url)
	}
	path, err := jsonschema.FileLoader{}.ToFile(url)
	if err != nil {
		return nil, err
	}
	rel, err := bundle.RelTo(l.root, path)
	if err != nil {
		out, _ := filepath.Rel(l.root, path)
		return nil, fmt.Errorf("a $ref leads to %s, which leaves the descriptor's directory", filepath.ToSlash(out))
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist", rel)
	}
	if err != nil {
		return nil, err
	}
	// JSON is YAML, so this reads schemas written in either.
	d, err := yamldoc.Parse(path, b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	doc := d.JSON()
	draft, err := checkDraft(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	l.record(rel, draft, doc)
	return doc, nil
}

func (l *loader) inline(ptr, draft string, doc any) { l.record(ptr, draft, doc) }

func (l *loader) record(key, draft string, doc any) {
	l.drafts[key] = draft
	l.docs[key] = doc
}

// drafts accepted in $schema (03-formats: 07, 2019-09 and 2020-12).
var supportedDrafts = map[string]string{
	"http://json-schema.org/draft-07/schema":       "07",
	"https://json-schema.org/draft/2019-09/schema": "2019-09",
	"https://json-schema.org/draft/2020-12/schema": "2020-12",
}

// checkDraft returns the draft a schema document declares, defaulting to
// 2020-12, or an error for drafts the portal doesn't support.
func checkDraft(doc any) (string, error) {
	m, ok := doc.(map[string]any)
	if !ok {
		return "2020-12", nil
	}
	s, ok := m["$schema"].(string)
	if !ok {
		return "2020-12", nil
	}
	if d, ok := supportedDrafts[strings.TrimSuffix(s, "#")]; ok {
		return d, nil
	}
	return "", fmt.Errorf("$schema %s is not supported; use draft-07, 2019-09 or 2020-12", s)
}

func fileURL(abs string) string {
	return (&gourl.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String()
}

func isRemote(ref string) bool {
	u, err := gourl.Parse(ref)
	return err == nil && u.Scheme != "" && u.Scheme != "file"
}

func cutFragment(ref string) (string, string, bool) {
	return strings.Cut(ref, "#")
}

// compileError turns a compiler error into a message that names files
// relative to the root instead of by absolute file:// URL.
func (l *loader) compileError(err error) error {
	var le *jsonschema.LoadURLError
	if errors.As(err, &le) {
		return le.Err // the loader's own errors already read well
	}
	var sve *jsonschema.SchemaValidationError
	var ve *jsonschema.ValidationError
	if errors.As(err, &sve) && errors.As(sve.Err, &ve) {
		var msgs []string
		for _, v := range yamldoc.Flatten(ve, yamldoc.Message) {
			msgs = append(msgs, strings.TrimPrefix(v.Pointer+": ", ": ")+v.Message)
		}
		return fmt.Errorf("%s is not a valid JSON Schema: %s", l.display(sve.URL), strings.Join(msgs, "; "))
	}
	return errors.New(strings.ReplaceAll(err.Error(), fileURL(l.root)+"/", ""))
}

// display names a schema URL by its path relative to the root.
func (l *loader) display(url string) string {
	url = strings.TrimSuffix(url, "#")
	if ptr, ok := l.names[url]; ok {
		return ptr
	}
	path, err := jsonschema.FileLoader{}.ToFile(url)
	if err != nil {
		return url
	}
	if rel, err := bundle.RelTo(l.root, path); err == nil {
		return rel
	}
	return url
}
