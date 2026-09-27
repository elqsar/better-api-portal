package lint

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/yamldoc"
)

// OpenAPITarget is what the openapi-default ruleset runs over.
type OpenAPITarget struct {
	Spec  *model.Spec
	Doc   *yamldoc.Doc   // the entry file
	Raw   map[string]any // the entry file as JSON
	Bytes []byte         // the entry file as read
	File  string         // the entry file's path, named in findings
	Dir   string         // its directory, for vacuum's $ref resolution
	// Environments are the descriptor's environment URLs for this API.
	Environments []string
}

// OpenAPI runs the openapi-default ruleset (docs/spec/04-governance.md §1):
// vacuum's recommended rules, then the org rules and the curated security
// subset, which are native.
func OpenAPI(t OpenAPITarget, cfg Config) []model.Finding {
	fs := runVacuum(t)
	return append(fs, run(openAPIRules, Target{Spec: t.Spec, File: t.File, Line: t.Doc.Line, openapi: &t}, cfg)...)
}

var openAPIRules = []rule{
	// operationId presence and uniqueness are vacuum's operation-operationId
	// and operation-operationId-unique; this only adds the naming convention.
	{"org-operation-id", model.SeverityError, orgOperationID},
	{"org-owner-contact", model.SeverityWarn, orgOwnerContact},
	{"org-problem-json", model.SeverityWarn, orgProblemJSON},
	{"org-no-version-in-path", model.SeverityWarn, orgNoVersionInPath},
	{"org-servers-match-env", model.SeverityWarn, orgServersMatchEnv},
	{"sec-operation-security", model.SeverityError, secOperationSecurity},
	{"sec-no-query-credentials", model.SeverityError, secNoQueryCredentials},
	{"sec-no-basic-auth", model.SeverityError, secNoBasicAuth},
	{"sec-https-servers", model.SeverityError, secHTTPSServers},
	{"sec-string-restricted", model.SeverityWarn, secStringRestricted},
	{"sec-integer-bounds", model.SeverityWarn, secIntegerBounds},
	{"sec-auth-responses", model.SeverityWarn, secAuthResponses},
}

var camelCase = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

func orgOperationID(t Target, _ Config, report func(ptr, msg string)) {
	for _, op := range t.Spec.Operations {
		if op.OperationID != "" && !camelCase.MatchString(op.OperationID) {
			report(op.Pointer+"/operationId", fmt.Sprintf("operationId %s must be camelCase, e.g. getOrder", op.OperationID))
		}
	}
}

func orgOwnerContact(t Target, _ Config, report func(ptr, msg string)) {
	if _, ok := obj(t.openapi.Raw["info"])["contact"]; !ok {
		report("/info", "info.contact is missing: say whom to ask about this API")
	}
}

func orgProblemJSON(t Target, _ Config, report func(ptr, msg string)) {
	seen := map[string]bool{}
	for _, op := range eachOperation(t.openapi.Raw) {
		responses := obj(op.v["responses"])
		for _, status := range sortedKeys(responses) {
			if !strings.HasPrefix(status, "4") && !strings.HasPrefix(status, "5") {
				continue
			}
			ptr := op.ptr + yamldoc.Pointer("responses", status)
			resp, at := localDeref(t.openapi.Raw, responses[status], ptr)
			if resp == nil || seen[at] {
				continue // external, or a shared response already reported
			}
			seen[at] = true
			content := obj(resp["content"])
			if len(content) == 0 {
				continue
			}
			if _, ok := content["application/problem+json"]; !ok {
				report(at, fmt.Sprintf("%s response content should be application/problem+json (RFC 9457)", status))
			}
		}
	}
}

var versionSegment = regexp.MustCompile(`^v[0-9]+$`)

func orgNoVersionInPath(t Target, _ Config, report func(ptr, msg string)) {
	for _, p := range sortedKeys(obj(t.openapi.Raw["paths"])) {
		for _, seg := range strings.Split(p, "/") {
			if versionSegment.MatchString(seg) {
				report(yamldoc.Pointer("paths", p), fmt.Sprintf("path %s contains the version %s: keep versions in the spec and routing, not in URLs", p, seg))
				break
			}
		}
	}
}

func orgServersMatchEnv(t Target, _ Config, report func(ptr, msg string)) {
	envs := t.openapi.Environments
	if len(envs) == 0 {
		return
	}
	for i, s := range list(t.openapi.Raw["servers"]) {
		u := str(obj(s)["url"])
		if u != "" && !slices.Contains(envs, strings.TrimSuffix(u, "/")) && !slices.Contains(envs, u) {
			report(fmt.Sprintf("/servers/%d/url", i), fmt.Sprintf("server %s is not one of the environments in portal.yaml", u))
		}
	}
}

func secOperationSecurity(t Target, _ Config, report func(ptr, msg string)) {
	global, _ := t.openapi.Raw["security"].([]any)
	for _, op := range eachOperation(t.openapi.Raw) {
		own, explicit := op.v["security"].([]any)
		effective := global
		if explicit {
			effective = own
		}
		if !public(effective) {
			continue
		}
		justification := strings.TrimSpace(str(op.v["x-portal-public"]))
		switch {
		case explicit && justification != "":
		case explicit:
			report(op.ptr, "the operation is public: justify it with x-portal-public")
		default:
			report(op.ptr, "the operation has no security: add a requirement, or declare security: [] with an x-portal-public justification")
		}
	}
}

// public reports whether a list of security requirements lets anonymous
// callers in: it is empty, or one alternative is the empty requirement.
func public(reqs []any) bool {
	if len(reqs) == 0 {
		return true
	}
	return slices.ContainsFunc(reqs, func(r any) bool { return len(obj(r)) == 0 })
}

func secNoQueryCredentials(t Target, _ Config, report func(ptr, msg string)) {
	eachScheme(t.openapi.Raw, func(name, ptr string, s map[string]any) {
		if str(s["type"]) == "apiKey" && str(s["in"]) == "query" {
			report(ptr, fmt.Sprintf("security scheme %s sends credentials in the query string, where they end up in logs: use a header", name))
		}
	})
}

func secNoBasicAuth(t Target, _ Config, report func(ptr, msg string)) {
	eachScheme(t.openapi.Raw, func(name, ptr string, s map[string]any) {
		if str(s["type"]) == "http" && strings.EqualFold(str(s["scheme"]), "basic") {
			report(ptr, fmt.Sprintf("security scheme %s uses HTTP basic authentication", name))
		}
	})
}

func secHTTPSServers(t Target, _ Config, report func(ptr, msg string)) {
	check := func(servers any, base string) {
		for i, s := range list(servers) {
			raw := str(obj(s)["url"])
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "http" {
				continue
			}
			switch u.Hostname() {
			case "localhost", "127.0.0.1", "::1":
				continue
			}
			report(fmt.Sprintf("%s/servers/%d/url", base, i), fmt.Sprintf("server %s uses http: use https", raw))
		}
	}
	check(t.openapi.Raw["servers"], "")
	paths := obj(t.openapi.Raw["paths"])
	for _, p := range sortedKeys(paths) {
		check(obj(paths[p])["servers"], yamldoc.Pointer("paths", p))
	}
	for _, op := range eachOperation(t.openapi.Raw) {
		check(op.v["servers"], op.ptr)
	}
}

func secStringRestricted(t Target, _ Config, report func(ptr, msg string)) {
	eachSchema(t.openapi.Raw, func(ptr string, s map[string]any) {
		if !hasType(s, "string") {
			return
		}
		for _, k := range []string{"maxLength", "pattern", "enum", "const", "format"} {
			if _, ok := s[k]; ok {
				return
			}
		}
		report(ptr, "string has no maxLength, pattern, enum or format: unbounded input is an injection and DoS risk")
	})
}

func secIntegerBounds(t Target, _ Config, report func(ptr, msg string)) {
	eachSchema(t.openapi.Raw, func(ptr string, s map[string]any) {
		if !hasType(s, "integer") {
			return
		}
		var missing []string
		if _, ok := s["format"]; !ok {
			missing = append(missing, "format")
		}
		if !hasAny(s, "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum") {
			missing = append(missing, "minimum or maximum")
		}
		if len(missing) > 0 {
			report(ptr, "integer has no "+strings.Join(missing, " and "))
		}
	})
}

func secAuthResponses(t Target, _ Config, report func(ptr, msg string)) {
	global, _ := t.openapi.Raw["security"].([]any)
	for _, op := range eachOperation(t.openapi.Raw) {
		effective := global
		if own, ok := op.v["security"].([]any); ok {
			effective = own
		}
		if public(effective) {
			continue
		}
		responses := obj(op.v["responses"])
		if !hasAny(responses, "401", "4XX") {
			report(op.ptr+"/responses", "the operation is secured but defines no 401 response")
		}
	}
}

// --- walking the raw document

type rawOp struct {
	ptr string
	v   map[string]any
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func eachOperation(raw map[string]any) []rawOp {
	var ops []rawOp
	paths := obj(raw["paths"])
	for _, p := range sortedKeys(paths) {
		item := obj(paths[p])
		for _, m := range httpMethods {
			if op := obj(item[m]); op != nil {
				ops = append(ops, rawOp{yamldoc.Pointer("paths", p, m), op})
			}
		}
	}
	return ops
}

func eachScheme(raw map[string]any, fn func(name, ptr string, s map[string]any)) {
	schemes := obj(obj(raw["components"])["securitySchemes"])
	for _, name := range sortedKeys(schemes) {
		if s := obj(schemes[name]); s != nil {
			fn(name, yamldoc.Pointer("components", "securitySchemes", name), s)
		}
	}
}

// eachSchema calls fn for every schema object defined in the entry file:
// components, parameters, headers and media types, and their subschemas.
// $refs are not followed, so each schema is visited where it is defined.
// Schemas in external files are not covered.
func eachSchema(raw map[string]any, fn func(ptr string, s map[string]any)) {
	var walk func(v any, ptr string)
	walk = func(v any, ptr string) {
		s := obj(v)
		if s == nil {
			return
		}
		if _, ok := s["$ref"]; ok {
			return
		}
		fn(ptr, s)
		props := obj(s["properties"])
		for _, name := range sortedKeys(props) {
			walk(props[name], ptr+yamldoc.Pointer("properties", name))
		}
		for _, k := range []string{"items", "additionalProperties", "not"} {
			walk(s[k], ptr+yamldoc.Pointer(k))
		}
		for _, k := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
			for i, sub := range list(s[k]) {
				walk(sub, fmt.Sprintf("%s/%s/%d", ptr, k, i))
			}
		}
	}
	media := func(content any, ptr string) {
		c := obj(content)
		for _, mt := range sortedKeys(c) {
			walk(obj(c[mt])["schema"], ptr+yamldoc.Pointer("content", mt, "schema"))
		}
	}
	headers := func(hs any, ptr string) {
		h := obj(hs)
		for _, name := range sortedKeys(h) {
			walk(obj(h[name])["schema"], ptr+yamldoc.Pointer("headers", name, "schema"))
		}
	}
	params := func(ps any, ptr string) {
		for i, p := range list(ps) {
			walk(obj(p)["schema"], fmt.Sprintf("%s/parameters/%d/schema", ptr, i))
		}
	}
	responses := func(rs any, ptr string) {
		r := obj(rs)
		for _, status := range sortedKeys(r) {
			base := ptr + yamldoc.Pointer(status)
			media(obj(r[status])["content"], base)
			headers(obj(r[status])["headers"], base)
		}
	}

	comp := obj(raw["components"])
	schemas := obj(comp["schemas"])
	for _, name := range sortedKeys(schemas) {
		walk(schemas[name], yamldoc.Pointer("components", "schemas", name))
	}
	cps := obj(comp["parameters"])
	for _, name := range sortedKeys(cps) {
		walk(obj(cps[name])["schema"], yamldoc.Pointer("components", "parameters", name, "schema"))
	}
	headers(comp["headers"], "/components")
	rbs := obj(comp["requestBodies"])
	for _, name := range sortedKeys(rbs) {
		media(obj(rbs[name])["content"], yamldoc.Pointer("components", "requestBodies", name))
	}
	responses(comp["responses"], "/components/responses")

	paths := obj(raw["paths"])
	for _, p := range sortedKeys(paths) {
		params(obj(paths[p])["parameters"], yamldoc.Pointer("paths", p))
	}
	for _, op := range eachOperation(raw) {
		params(op.v["parameters"], op.ptr)
		media(obj(op.v["requestBody"])["content"], op.ptr+"/requestBody")
		responses(op.v["responses"], op.ptr+"/responses")
	}
}

// localDeref follows local ("#/...") $refs within raw. It returns nil for
// refs into other files.
func localDeref(raw map[string]any, v any, ptr string) (map[string]any, string) {
	for range 32 {
		m := obj(v)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m, ptr
		}
		if !strings.HasPrefix(ref, "#") {
			return nil, ptr
		}
		ptr = ref[1:]
		v = lookup(raw, ptr)
	}
	return nil, ptr
}

func lookup(doc any, ptr string) any {
	cur := doc
	for _, tok := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		if tok == "" && ptr == "" {
			break
		}
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		cur = obj(cur)[tok]
	}
	return cur
}

func hasType(s map[string]any, t string) bool {
	switch v := s["type"].(type) {
	case string:
		return v == t
	case []any:
		return slices.Contains(v, any(t))
	}
	return false
}

func hasAny(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
