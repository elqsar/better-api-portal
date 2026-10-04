package agentdoc

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/schematree"
)

// Entry is an API in the llms.txt index.
type Entry struct {
	ID, Kind, Title, Owner, Lifecycle, Version string
	Description                                string
}

// Catalogue renders llms.txt (https://llmstxt.org): the portal, how to use
// it, and every API that isn't retired, by team.
func Catalogue(name string, entries []Entry, u URLs) string {
	w := &writer{}
	w.heading(1, name)
	w.line("> The company's catalogue of HTTP APIs (OpenAPI) and events (CloudEvents): " +
		"who owns each API, its contract, and who depends on it. Look contracts up here " +
		"rather than guessing payloads or endpoints.")
	w.blank()
	w.line("Every page is Markdown. An API page lists its operations or event types, each with " +
		"its own page that inlines the request, response or payload schema.")
	w.blank()
	w.linef("- Search: %s (words, a quoted phrase, -word to exclude)", u.Search(""))
	w.linef("- An API's latest version: %s/apis/{api-id}.md", u.Base)
	w.linef("- An event type: %s/events/{type}.md", u.Base)

	var teams []string
	byTeam := map[string][]Entry{}
	for _, e := range entries {
		if e.Lifecycle == "retired" {
			continue
		}
		if _, ok := byTeam[e.Owner]; !ok {
			teams = append(teams, e.Owner)
		}
		byTeam[e.Owner] = append(byTeam[e.Owner], e)
	}
	slices.Sort(teams)
	for _, team := range teams {
		w.heading(2, team)
		es := byTeam[team]
		slices.SortFunc(es, func(a, b Entry) int { return strings.Compare(a.ID, b.ID) })
		for _, e := range es {
			title := e.Title
			if title == "" {
				title = e.ID
			}
			note := []string{code(e.ID), kindName(e.Kind), e.Version}
			if e.Lifecycle != "" && e.Lifecycle != "production" {
				note = append(note, e.Lifecycle)
			}
			item := fmt.Sprintf("- [%s](%s): %s", title, u.API(e.ID, ""), strings.Join(note, ", "))
			if d := summary(e.Description); d != "" {
				item += ". " + sentence(d)
			}
			w.line(item)
		}
	}
	if len(teams) > 0 {
		w.heading(2, "Optional")
		for _, team := range teams {
			w.linef("- [Every API of %s in one file](%s)", team, u.Full(team))
		}
	}
	return w.String()
}

// maxSummary caps an API's description in the index, in runes.
const maxSummary = 200

// summary is a description's first paragraph on one line, shortened to
// maxSummary.
func summary(desc string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(desc), "\n\n")
	s := []rune(oneLine(para))
	if len(s) <= maxSummary {
		return string(s)
	}
	cut := string(s[:maxSummary])
	if i := strings.LastIndex(cut, " "); i > maxSummary/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

func kindName(kind string) string {
	switch kind {
	case "openapi":
		return "OpenAPI"
	case "cloudevents":
		return "CloudEvents"
	case "asyncapi":
		return "AsyncAPI"
	}
	return kind
}

// APIPage renders an API's version: what it is, its operations or event
// types, and its dependencies. Full adds every operation's and event
// type's page, for llms-full.txt; level shifts its headings down.
func APIPage(a *API, v *Version, u URLs, full bool, level int) string {
	w := &writer{level: level}
	spec := v.Spec
	w.heading(1, a.Name())
	w.line(facts("API id", code(a.ID), "Kind", kindName(a.Kind), "Version", a.Version, "Lifecycle", a.Lifecycle))
	if a.Lifecycle == "deprecated" {
		w.blank()
		msg := "> **Deprecated.** Don't build new integrations on it."
		if a.Sunset != "" {
			msg = "> **Deprecated**, sunset " + a.Sunset + ". Don't build new integrations on it."
		}
		w.line(msg)
	}
	w.para(spec.Description)

	w.blank()
	w.line(facts("Owner", a.Owner, "System", a.System, "Repo", a.Repo))
	if len(a.Tags) > 0 {
		w.line("Tags: " + strings.Join(a.Tags, ", "))
	}
	if len(spec.Servers) > 0 || len(a.Environments) > 0 {
		w.heading(2, "Environments")
		for _, e := range a.Environments {
			w.line("- " + facts(e.Name, strings.TrimSpace(e.URL+" "+brokerNote(e.Broker))))
		}
		for _, s := range spec.Servers {
			if !slices.ContainsFunc(a.Environments, func(e Environment) bool { return e.URL == s }) {
				w.line("- server: " + s)
			}
		}
	}
	if len(a.Links) > 0 {
		w.heading(2, "Links")
		for _, l := range a.Links {
			w.linef("- [%s](%s)", l.Title, l.URL)
		}
	}

	if len(spec.Operations) > 0 {
		w.heading(2, "Operations")
		for _, op := range spec.Operations {
			item := fmt.Sprintf("- [%s](%s)", code(op.Method+" "+op.Path),
				u.Operation(a.ID, a.Version, OperationKey(op.Method, op.Path, op.OperationID)))
			if op.Summary != "" {
				item += ": " + oneLine(op.Summary)
			}
			if op.Deprecated {
				item += " (deprecated)"
			}
			w.line(item)
		}
	}
	if len(spec.Messages) > 0 {
		w.heading(2, "Event types")
		for _, m := range spec.Messages {
			item := fmt.Sprintf("- [%s](%s) (%s)", code(m.Key), u.Event(m.Key), m.Role)
			if m.Summary != "" {
				item += ": " + oneLine(m.Summary)
			}
			if m.Deprecated {
				item += " (deprecated)"
			}
			w.line(item)
		}
	}
	if len(a.Consumers) > 0 {
		w.heading(2, "Consumers")
		writeDeps(w, a.Consumers, false)
	}
	if len(a.Consumes) > 0 {
		w.heading(2, "Consumes")
		writeDeps(w, a.Consumes, true)
	}

	if full {
		for _, op := range spec.Operations {
			w.blank()
			w.b.WriteString(OperationPage(a, v, op, u, level+1))
		}
		for _, m := range spec.Messages {
			w.blank()
			w.b.WriteString(MessagePage(a, v, m, nil, u, level+1))
		}
	}
	return w.String()
}

func brokerNote(broker string) string {
	if broker == "" {
		return ""
	}
	return "broker " + broker
}

// writeDeps lists dependencies: what is consumed (to) or who consumes.
func writeDeps(w *writer, deps []Dependency, to bool) {
	for _, d := range deps {
		w.line("- " + depItem(d, to, true))
	}
}

// depItem describes a dependency. withWhat adds what is consumed.
func depItem(d Dependency, to, withWhat bool) string {
	what := "everything"
	if len(d.Types) > 0 {
		var ts []string
		for _, t := range d.Types {
			ts = append(ts, code(t))
		}
		what = strings.Join(ts, ", ")
	}
	if to {
		return what + " from " + code(d.To)
	}
	who := d.Repo
	if len(d.APIs) > 0 {
		who += " (" + strings.Join(d.APIs, ", ") + ")"
	}
	if d.Owner != "" {
		who += ", " + d.Owner
	}
	if !withWhat {
		return who
	}
	return who + ": " + what
}

// OperationPage renders one HTTP operation with its parameters, request
// body and responses, schemas inlined.
func OperationPage(a *API, v *Version, op model.Operation, u URLs, level int) string {
	w := &writer{level: level}
	title := code(op.Method + " " + op.Path)
	if op.Summary != "" {
		title += ": " + oneLine(op.Summary)
	}
	w.heading(1, title)
	w.line(facts("operationId", codeOr(op.OperationID),
		"API", fmt.Sprintf("[%s](%s) %s", a.Name(), u.API(a.ID, a.Version), a.Version),
		"Auth", v.security(op.Security)))
	if op.Deprecated {
		w.blank()
		w.line("> **Deprecated.** Don't use it in new code.")
	}
	w.para(op.Description)
	raw := v.raw(op.Pointer)

	if len(op.Parameters) > 0 {
		w.heading(2, "Parameters")
		for _, p := range op.Parameters {
			w.line(v.param(p, op.Pointer))
		}
	}
	if len(op.Request) > 0 {
		w.heading(2, "Request body")
		body := v.rawChild(raw, op.Pointer, "requestBody")
		if body["required"] == true {
			w.line("Required.")
		}
		if d, _ := body["description"].(string); d != "" {
			w.para(d)
		}
		for _, mt := range sortedKeys(op.Request) {
			w.blank()
			w.line(code(mt) + ":")
			w.blank()
			w.writeSchema(v, op.Request[mt])
		}
	}
	if len(op.Responses) > 0 {
		w.heading(2, "Responses")
		for _, g := range v.responseGroups(op, raw) {
			head := strings.Join(g.statuses, ", ")
			if g.description != "" {
				head += ": " + oneLine(g.description)
			}
			w.heading(3, head)
			if len(g.content) == 0 {
				w.line("No body.")
			}
			for _, mt := range sortedKeys(g.content) {
				w.blank()
				w.line(code(mt) + ":")
				w.blank()
				w.writeSchema(v, g.content[mt])
			}
		}
	}
	return w.String()
}

func codeOr(s string) string {
	if s == "" {
		return ""
	}
	return code(s)
}

// security describes the schemes an operation needs.
func (v *Version) security(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	schemes := v.raw("/components/securitySchemes")
	var out []string
	for _, n := range names {
		s, _ := schemes[n].(map[string]any)
		var parts []string
		for _, k := range []string{"type", "scheme", "bearerFormat", "in", "name"} {
			if x, _ := s[k].(string); x != "" {
				parts = append(parts, x)
			}
		}
		if len(parts) > 0 {
			n += " (" + strings.Join(parts, " ") + ")"
		}
		out = append(out, n)
	}
	return strings.Join(out, ", ")
}

// rawChild returns the object at key of the raw operation, following refs.
func (v *Version) rawChild(raw map[string]any, opPointer, key string) map[string]any {
	if _, ok := raw[key]; !ok {
		return nil
	}
	return v.raw(opPointer + "/" + key)
}

// param describes a parameter: its place, its schema's type and facts, and
// its description from the raw document.
func (v *Version) param(p model.Parameter, opPointer string) string {
	tags := []string{p.In}
	var rest []string
	if p.Required {
		tags = append(tags, "required")
	}
	if desc := v.paramDescription(p, opPointer); desc != "" {
		rest = append(rest, sentence(oneLine(desc)))
	}
	if p.Schema != "" {
		if n, err := schematree.Build(v.schema(p.Schema)); err == nil {
			if n.Type != "" {
				tags = append(tags, n.Type)
			}
			if len(n.Enum) > 0 {
				rest = append(rest, "One of "+strings.Join(n.Enum, ", ")+".")
			}
			if len(n.Facts) > 0 {
				rest = append(rest, strings.Join(n.Facts, "; ")+".")
			}
		}
	}
	s := "- " + code(p.Name) + " (" + strings.Join(tags, ", ") + ")"
	if len(rest) > 0 {
		s += ": " + strings.Join(rest, " ")
	}
	return s
}

// paramDescription finds the parameter's description: on the operation's
// own parameters, then its path's.
func (v *Version) paramDescription(p model.Parameter, opPointer string) string {
	pathPointer := opPointer[:max(strings.LastIndex(opPointer, "/"), 0)]
	for _, at := range []string{opPointer, pathPointer} {
		list, _ := v.raw(at)["parameters"].([]any)
		for i := range list {
			raw := v.raw(fmt.Sprintf("%s/parameters/%d", at, i))
			if raw["name"] == p.Name && raw["in"] == p.In {
				d, _ := raw["description"].(string)
				return d
			}
		}
	}
	return ""
}

type responseGroup struct {
	statuses    []string
	description string
	content     map[string]string
}

// responseGroups merges responses that are the same but for their status,
// such as the usual 401/404/409 problem responses.
func (v *Version) responseGroups(op model.Operation, raw map[string]any) []responseGroup {
	responses, _ := raw["responses"].(map[string]any)
	var groups []responseGroup
	for _, status := range sortedKeys(op.Responses) {
		desc := ""
		if _, ok := responses[status]; ok {
			r := v.raw(op.Pointer + "/responses/" + escapeToken(status))
			desc, _ = r["description"].(string)
		}
		content := op.Responses[status]
		i := slices.IndexFunc(groups, func(g responseGroup) bool {
			return g.description == desc && sameContent(g.content, content)
		})
		if i < 0 {
			groups = append(groups, responseGroup{description: desc, content: content})
			i = len(groups) - 1
		}
		groups[i].statuses = append(groups[i].statuses, status)
	}
	return groups
}

func sameContent(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func escapeToken(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// MessagePage renders one event type: envelope, bindings, payload schema,
// examples and consumers.
func MessagePage(a *API, v *Version, m model.Message, consumers []Dependency, u URLs, level int) string {
	w := &writer{level: level}
	w.heading(1, code(m.Key))
	if m.Summary != "" {
		w.line(sentence(oneLine(m.Summary)))
		w.blank()
	}
	w.line(facts("API", fmt.Sprintf("[%s](%s) %s", a.Name(), u.API(a.ID, a.Version), a.Version),
		"Role", string(m.Role), "Owner", a.Owner))
	if m.Deprecated {
		w.blank()
		w.line("> **Deprecated.** Don't use it in new code.")
	}
	w.para(m.Description)

	w.heading(2, "Envelope (CloudEvents attributes)")
	w.line("- `type`: " + code(m.Key))
	for _, attr := range [][2]string{
		{"source", m.CE.Source}, {"subject", m.CE.Subject},
		{"datacontenttype", m.CE.DataContentType}, {"dataschema", m.CE.DataSchemaURI},
	} {
		if attr[1] != "" {
			w.line("- " + code(attr[0]) + ": " + code(attr[1]))
		}
	}
	for _, name := range sortedKeys(m.CE.Extensions) {
		e := m.CE.Extensions[name]
		tags := []string{"extension"}
		if e.Type != "" {
			tags = append(tags, e.Type)
		}
		if e.Required {
			tags = append(tags, "required")
		}
		item := "- " + code(name) + " (" + strings.Join(tags, ", ") + ")"
		if e.Description != "" {
			item += ": " + sentence(oneLine(e.Description))
		}
		w.line(item)
	}

	if len(m.Bindings) > 0 {
		w.heading(2, "Bindings")
		for _, b := range m.Bindings {
			item := "- " + b.Protocol + " " + code(b.Address)
			var props []string
			for _, k := range sortedKeys(b.Props) {
				props = append(props, k+": "+jsonValue(b.Props[k]))
			}
			if len(props) > 0 {
				item += " (" + strings.Join(props, "; ") + ")"
			}
			w.line(item)
		}
	}

	w.heading(2, "Payload (data)")
	w.writeSchema(v, m.Payload)

	if len(m.Examples) > 0 {
		w.heading(2, "Examples")
		for _, e := range m.Examples {
			if e.Name != "" {
				w.line(e.Name + ":")
				w.blank()
			}
			b, err := json.MarshalIndent(e.Data, "", "  ")
			if err != nil {
				continue
			}
			w.line("```json")
			w.line(string(b))
			w.line("```")
			w.blank()
		}
	}
	if len(consumers) > 0 {
		w.heading(2, "Consumers")
		for _, d := range consumers {
			w.line("- " + depItem(d, false, false))
		}
	}
	return w.String()
}

// jsonValue shows a value: strings as they are, the rest as JSON.
func jsonValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Hit is a search result.
type Hit struct {
	APIID, Version, Kind, Ref, Title string
	APIKind, Owner, Lifecycle        string
	// Snippet is the matched text with matches in bold; Marked says
	// whether it marks anything (a title-only match doesn't).
	Snippet string
	Marked  bool
}

// SearchPage lists search hits, each with a link to its page.
func SearchPage(q string, hits []Hit, truncated bool, u URLs) string {
	w := &writer{}
	w.heading(1, "Search: "+q)
	switch {
	case len(hits) == 0:
		w.line("Nothing matched. Try fewer or other words; a typo in a title is forgiven, in other text it isn't.")
		return w.String()
	case truncated:
		w.linef("The first %d results, best first. Narrow with &kind=operation|message|schema|api, &team= or &api=.", len(hits))
	default:
		w.linef("%d results, best first.", len(hits))
	}
	w.blank()
	for _, h := range hits {
		var link, label string
		switch h.Kind {
		case "message":
			label, link = code(h.Ref), u.Event(h.Ref)
		case "operation":
			method, path, _ := strings.Cut(h.Ref, " ")
			label, link = code(h.Ref), u.Operation(h.APIID, h.Version, OperationKey(method, path, ""))
		case "api":
			label, link = cmpOr(h.Title, h.APIID), u.API(h.APIID, h.Version)
		default: // a schema: its API's page links where it's used
			label, link = code(cmpOr(h.Title, h.Ref)), u.API(h.APIID, h.Version)
		}
		where := []string{h.Kind}
		if h.Kind == "api" {
			where = []string{kindName(h.APIKind) + " API"}
		}
		where = append(where, h.APIID+" "+h.Version, h.Owner)
		if h.Lifecycle == "deprecated" || h.Lifecycle == "retired" {
			where = append(where, h.Lifecycle)
		}
		item := fmt.Sprintf("- [%s](%s) (%s)", label, link, strings.Join(where, ", "))
		if h.Marked && h.Snippet != "" {
			item += ": " + h.Snippet
		}
		w.line(item)
	}
	return w.String()
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
