package yamldoc

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// CompileEmbedded compiles the schema file name from fsys, registered under
// id (the file's $id) so that it doesn't resolve against the working directory.
func CompileEmbedded(fsys fs.FS, name, id string) (*jsonschema.Schema, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(id, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", name, err)
	}
	return s, nil
}

var printer = message.NewPrinter(language.English)

// Message renders a validation error with the library's default wording.
func Message(e *jsonschema.ValidationError) string {
	return e.ErrorKind.LocalizedString(printer)
}

// Violation is one schema error at one instance location.
type Violation struct {
	Pointer string
	Message string
}

// Validate returns the leaf violations of doc against s, merged so that each
// instance location is reported once. describe renders each leaf; pass
// Message for the default wording.
func Validate(s *jsonschema.Schema, doc any, describe func(*jsonschema.ValidationError) string) ([]Violation, error) {
	err := s.Validate(doc)
	if err == nil {
		return nil, nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil, err
	}
	return Flatten(ve, describe), nil
}

// Flatten returns the leaf causes of e, merged so that each instance
// location is reported once.
func Flatten(e *jsonschema.ValidationError, describe func(*jsonschema.ValidationError) string) []Violation {
	byPtr := map[string][]string{}
	var order []string
	var collect func(e *jsonschema.ValidationError)
	collect = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				collect(c)
			}
			return
		}
		p := Pointer(e.InstanceLocation...)
		msg := describe(e)
		if _, seen := byPtr[p]; !seen {
			order = append(order, p)
		}
		if !slices.Contains(byPtr[p], msg) {
			byPtr[p] = append(byPtr[p], msg)
		}
	}
	collect(e)
	vs := make([]Violation, 0, len(order))
	for _, p := range order {
		vs = append(vs, Violation{Pointer: p, Message: strings.Join(byPtr[p], "; ")})
	}
	return vs
}

// ValidationViolations flattens the error returned by Schema.Validate. Errors
// other than validation failures are returned as is.
func ValidationViolations(err error) ([]Violation, error) {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil, err
	}
	return Flatten(ve, Message), nil
}
