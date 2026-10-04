// Package agentdoc renders the catalogue as Markdown for AI agents: an
// llms.txt index, and one compact, self-contained page per API, operation
// and event type, with schemas inlined as field lists.
//
// It knows nothing about HTTP or the store: callers pass in what the portal
// knows about an API (API) and its parsed version (Version).
package agentdoc

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// API is what the portal knows about an API beyond its spec.
type API struct {
	ID, Kind, Title, Owner, Lifecycle string
	System, Repo                      string
	Sunset                            string // a date, if the API is deprecated
	Version                           string // the version rendered
	Tags                              []string
	Links                             []Link
	Environments                      []Environment
	// Consumes lists what the API's repo consumes; Consumers who consume
	// the API.
	Consumes, Consumers []Dependency
}

// Name is the API's title, or its id if it has none.
func (a *API) Name() string {
	if a.Title != "" {
		return a.Title
	}
	return a.ID
}

// Link is a titled URL from the descriptor.
type Link struct{ Title, URL string }

// Environment is where an API runs.
type Environment struct{ Name, URL, Broker string }

// Dependency is one repo consuming an API: all of it, or the listed types.
type Dependency struct {
	Repo  string
	Owner string   // the consuming repo's team
	APIs  []string // the consuming APIs of the repo
	To    string   // the API consumed
	Types []string // empty means everything from To
}

// URLs builds the links between pages.
type URLs struct {
	Base string // the portal's public URL, without a trailing slash
}

// API is an API's page; version "" means the latest.
func (u URLs) API(id, version string) string {
	if version == "" {
		return u.Base + "/apis/" + url.PathEscape(id) + ".md"
	}
	return u.Base + "/apis/" + url.PathEscape(id) + "/versions/" + url.PathEscape(version) + ".md"
}

// Operation is an operation's page.
func (u URLs) Operation(id, version, key string) string {
	return u.Base + "/apis/" + url.PathEscape(id) + "/versions/" + url.PathEscape(version) +
		"/operations/" + url.PathEscape(key) + ".md"
}

// Event is an event type's page.
func (u URLs) Event(typ string) string {
	return u.Base + "/events/" + url.PathEscape(typ) + ".md"
}

// Search is the search page for query q ("" for a template).
func (u URLs) Search(q string) string {
	if q == "" {
		return u.Base + "/search.md?q={words}"
	}
	return u.Base + "/search.md?q=" + url.QueryEscape(q)
}

// Full is the concatenated docs of one team's APIs.
func (u URLs) Full(team string) string {
	return u.Base + "/llms-full.txt?team=" + url.QueryEscape(team)
}

var nonSlug = regexp.MustCompile(`[^A-Za-z0-9]+`)

// OperationKey names an operation in URLs: its operationId, or its method
// and path ("get-orders-orderId") if it has none.
func OperationKey(method, path, operationID string) string {
	if operationID != "" {
		return operationID
	}
	return strings.ToLower(method) + "-" + strings.Trim(nonSlug.ReplaceAllString(path, "-"), "-")
}

// writer builds Markdown. Level shifts every heading down, so pages can
// be nested in llms-full.txt.
type writer struct {
	b     strings.Builder
	level int
}

func (w *writer) line(s string) { w.b.WriteString(s + "\n") }

func (w *writer) linef(format string, args ...any) { w.line(fmt.Sprintf(format, args...)) }

func (w *writer) blank() {
	s := w.b.String()
	if s != "" && !strings.HasSuffix(s, "\n\n") {
		w.b.WriteString("\n")
	}
}

// heading writes a heading at depth (1 for the page title) below level.
func (w *writer) heading(depth int, text string) {
	w.blank()
	w.line(strings.Repeat("#", min(depth+w.level, 6)) + " " + text)
	w.blank()
}

// para writes a text block as it is: descriptions are Markdown already.
func (w *writer) para(s string) {
	if s = strings.TrimSpace(s); s != "" {
		w.blank()
		w.line(s)
		w.blank()
	}
}

func (w *writer) String() string { return strings.TrimRight(w.b.String(), "\n") + "\n" }

// code quotes s as inline code.
func code(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// facts joins non-empty "label: value" pairs.
func facts(pairs ...string) string {
	var out []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			out = append(out, pairs[i]+": "+pairs[i+1])
		}
	}
	return strings.Join(out, " · ")
}
