package lint

import (
	"fmt"
	"mime"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// CloudEvents runs the portal-native cloudevents-default ruleset. It works on
// the normalised model, so it applies to any source of messages.
func CloudEvents(t Target, cfg Config) []model.Finding {
	return run(cloudEventsRules, t, cfg)
}

var cloudEventsRules = []rule{
	{"ce-type-format", model.SeverityError, typeFormat},
	{"ce-type-prefix", model.SeverityError, typePrefix},
	{"ce-examples-valid", model.SeverityError, examplesValid},
	{"ce-binding-present", model.SeverityError, bindingPresent},
	{"ce-kafka-key", model.SeverityWarn, kafkaKey},
	{"ce-type-major-matches-schema", model.SeverityWarn, typeMajorMatchesSchema},
	{"ce-description", model.SeverityWarn, description},
	{"ce-json-only", model.SeverityError, jsonOnly},
}

var typeRe = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+\.v([0-9]+)$`)

func typeFormat(t Target, _ Config, report func(ptr, msg string)) {
	for _, m := range t.Spec.Messages {
		if !typeRe.MatchString(m.Key) {
			report(m.Pointer+"/type", fmt.Sprintf("type %s must be reverse-DNS, lower-case and end in a major version such as .v1", m.Key))
		}
	}
}

func typePrefix(t Target, cfg Config, report func(ptr, msg string)) {
	if cfg.EventTypePrefix == "" {
		return
	}
	for _, m := range t.Spec.Messages {
		if !strings.HasPrefix(m.Key, cfg.EventTypePrefix) {
			report(m.Pointer+"/type", fmt.Sprintf("type %s must start with %s", m.Key, cfg.EventTypePrefix))
		}
	}
}

func examplesValid(t Target, _ Config, report func(ptr, msg string)) {
	for _, m := range t.Spec.Messages {
		schema := t.Payloads[m.Key]
		if schema == nil {
			continue // ce-dataschema-resolves already reported it
		}
		for j, ex := range m.Examples {
			err := schema.Validate(ex.Data)
			if err == nil {
				continue
			}
			name := ex.Name
			if name == "" {
				name = "#" + strconv.Itoa(j)
			}
			base := m.Pointer + "/examples/" + strconv.Itoa(j) + "/data"
			vs, verr := yamldoc.ValidationViolations(err)
			if verr != nil {
				report(base, fmt.Sprintf("example %s: %v", name, verr))
				continue
			}
			for _, v := range vs {
				report(base+v.Pointer, fmt.Sprintf("example %s does not match the dataschema: %s", name, v.Message))
			}
		}
	}
}

func bindingPresent(t Target, _ Config, report func(ptr, msg string)) {
	for _, m := range t.Spec.Messages {
		if len(m.Bindings) == 0 {
			report(m.Pointer, fmt.Sprintf("%s has no bindings and there are no default bindings: declare where it is carried", m.Key))
		}
	}
}

func kafkaKey(t Target, _ Config, report func(ptr, msg string)) {
	seen := map[string]bool{} // a default binding is shared by many messages
	for _, m := range t.Spec.Messages {
		for _, b := range m.Bindings {
			if b.Protocol != "kafka" || b.Props["key"] != nil || seen[b.Pointer] {
				continue
			}
			seen[b.Pointer] = true
			report(b.Pointer, fmt.Sprintf("kafka binding to %s has no key: ordering depends on it", b.Address))
		}
	}
}

// majorRe finds a major version at the end of a schema file name or $id, as in
// order-created.v1.json or https://…/order-created/v2.
var majorRe = regexp.MustCompile(`(?:^|[./_-])v([0-9]+)(?:\.(?:json|ya?ml))?$`)

func typeMajorMatchesSchema(t Target, _ Config, report func(ptr, msg string)) {
	entry := ""
	if len(t.Spec.Files) > 0 {
		entry = t.Spec.Files[0]
	}
	for _, m := range t.Spec.Messages {
		tm := typeRe.FindStringSubmatch(m.Key)
		if tm == nil || m.Payload == "" {
			continue
		}
		declared, where := schemaMajor(t.Spec, m.Payload, entry)
		if declared != "" && declared != tm[2] {
			report(m.Pointer+"/dataschema", fmt.Sprintf("type %s is major version %s, but its dataschema %s declares v%s",
				m.Key, tm[2], where, declared))
		}
	}
}

// schemaMajor returns the major version a payload schema declares, and where.
func schemaMajor(spec *model.Spec, payload, entry string) (string, string) {
	file, _, _ := strings.Cut(payload, "#")
	if file != entry { // inline schemas have no file name of their own
		if mm := majorRe.FindStringSubmatch(path.Base(file)); mm != nil {
			return mm[1], path.Base(file)
		}
	}
	s := spec.Schemas[payload]
	if s == nil {
		s = spec.Schemas[file]
	}
	if s == nil {
		return "", ""
	}
	doc, _ := s.Doc.(map[string]any)
	if id, ok := doc["$id"].(string); ok {
		if mm := majorRe.FindStringSubmatch(strings.TrimSuffix(id, "#")); mm != nil {
			return mm[1], "$id " + id
		}
	}
	return "", ""
}

func description(t Target, _ Config, report func(ptr, msg string)) {
	for _, m := range t.Spec.Messages {
		if m.Role == model.RoleProduces && strings.TrimSpace(m.Description) == "" {
			report(m.Pointer, fmt.Sprintf("%s has no description: say when it is emitted and what consumers can rely on", m.Key))
		}
	}
}

func jsonOnly(t Target, _ Config, report func(ptr, msg string)) {
	for _, m := range t.Spec.Messages {
		mt, _, err := mime.ParseMediaType(m.CE.DataContentType)
		if err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json")) {
			continue
		}
		report(m.Pointer, fmt.Sprintf("datacontenttype %s is not supported: only JSON payloads (application/json or +json) are, for now", m.CE.DataContentType))
	}
}
