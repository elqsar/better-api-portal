package descriptor

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"better-api-portal/internal/model"
)

// sniff identifies the kind of a spec file from its top-level version key
// (openapi, asyncapi, eventcatalog). It returns "" and a finding when the
// file is readable but not a supported format.
func sniff(path string) (Kind, *model.Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", &model.Finding{
			RuleID:   "spec-syntax",
			Severity: model.SeverityError,
			Message:  "not valid YAML or JSON: " + err.Error(),
			File:     path,
			Line:     yamlErrorLine(err),
		}, nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", nil, nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		unsupported := func(msg string) (Kind, *model.Finding, error) {
			return "", &model.Finding{
				RuleID:   "spec-unsupported-version",
				Severity: model.SeverityError,
				Message:  msg,
				File:     path,
				Pointer:  "/" + k.Value,
				Line:     k.Line,
			}, nil
		}
		switch k.Value {
		case "swagger":
			return unsupported(fmt.Sprintf("Swagger %s is not supported; convert it to OpenAPI 3.1 or 3.0 (e.g. with swagger2openapi)", v.Value))
		case "openapi":
			if strings.HasPrefix(v.Value, "3.0.") || strings.HasPrefix(v.Value, "3.1.") {
				return KindOpenAPI, nil, nil
			}
			return unsupported(fmt.Sprintf("OpenAPI %s is not supported; use 3.0.x or 3.1.x", v.Value))
		case "asyncapi":
			if strings.HasPrefix(v.Value, "3.") {
				return KindAsyncAPI, nil, nil
			}
			if strings.HasPrefix(v.Value, "2.") {
				return unsupported(fmt.Sprintf("AsyncAPI %s is not supported; upgrade to AsyncAPI 3.0 (see https://www.asyncapi.com/docs/migration/migrating-to-v3)", v.Value))
			}
			return unsupported(fmt.Sprintf("AsyncAPI %s is not supported; use 3.0", v.Value))
		case "eventcatalog":
			if v.Value == "1.0" {
				return KindCloudEvents, nil, nil
			}
			return unsupported(fmt.Sprintf("eventcatalog %q is not supported; use \"1.0\"", v.Value))
		}
	}
	return "", nil, nil
}
