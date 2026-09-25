package descriptor

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"better-api-portal/docs/spec/schemas"
)

const (
	schemaFile = "portal.schema.json"
	schemaID   = "https://api-portal.dev/schemas/portal/v1.json" // $id in the file
)

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	f, err := schemas.FS.Open(schemaFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", schemaFile, err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(schemaID, doc); err != nil {
		return nil, err
	}
	return c.Compile(schemaID)
})

var printer = message.NewPrinter(language.English)

// violation is one schema error at one instance location.
type violation struct {
	pointer string
	message string
}

// validateSchema returns the leaf violations of doc against the descriptor
// schema, merged so that each instance location is reported once.
func validateSchema(doc any) ([]violation, error) {
	sch, err := compiled()
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", schemaFile, err)
	}
	err = sch.Validate(doc)
	if err == nil {
		return nil, nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return nil, err
	}
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
		p := pointer(e.InstanceLocation)
		msg := describe(e)
		if _, seen := byPtr[p]; !seen {
			order = append(order, p)
		}
		if !slices.Contains(byPtr[p], msg) {
			byPtr[p] = append(byPtr[p], msg)
		}
	}
	collect(ve)
	vs := make([]violation, 0, len(order))
	for _, p := range order {
		vs = append(vs, violation{pointer: p, message: strings.Join(byPtr[p], "; ")})
	}
	return vs, nil
}

// describe renders an error, replacing the library's messages for the
// descriptor's conditional rules, which would otherwise read as "not failed".
func describe(e *jsonschema.ValidationError) string {
	kw := e.ErrorKind.KeywordPath()
	switch {
	case strings.HasSuffix(e.SchemaURL, "#/$defs/api/allOf/0/then"):
		return "compatibility is only allowed for asyncapi and cloudevents APIs"
	case strings.HasSuffix(e.SchemaURL, "#/$defs/api/allOf/1/then"):
		return "sunset is only allowed when lifecycle is deprecated"
	case strings.HasSuffix(e.SchemaURL, "/environments/items") && slices.Equal(kw, []string{"oneOf"}):
		return "an environment needs exactly one of url or broker"
	}
	return e.ErrorKind.LocalizedString(printer)
}
