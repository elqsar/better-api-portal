package descriptor

import (
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"better-api-portal/docs/spec/schemas"
	"better-api-portal/internal/yamldoc"
)

var compiled = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return yamldoc.CompileEmbedded(schemas.FS, "portal.schema.json", "https://api-portal.dev/schemas/portal/v1.json")
})

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
	return yamldoc.Message(e)
}
