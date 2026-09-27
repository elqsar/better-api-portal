package web

import (
	"cmp"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"better-api-portal/internal/store"
)

// searchLimit is how many hits are grouped by API; perGroup of them are
// shown for each API unless the search is narrowed to it.
const (
	searchLimit = 200
	perGroup    = 5
)

var searchKinds = []option{{"operation", "Operations"}, {"message", "Event types"}, {"schema", "Schemas"}, {"api", "APIs"}}

type searchData struct {
	Query  store.SearchQuery
	Teams  []option
	Kinds  []option
	Groups []searchGroup
	Hits   int
	// Truncated: there may be more hits than searchLimit.
	Truncated bool
}

// searchGroup is one API's hits. The API's own document, if it matched,
// is the group's heading rather than a hit.
type searchGroup struct {
	APIID, APIKind, Semver, Owner, Lifecycle string
	Title                                    string
	Snippet                                  template.HTML
	Hits                                     []searchHit
	More                                     int
	MoreURL                                  string
}

type searchHit struct {
	Kind, Ref, URL string
	Snippet        template.HTML
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	d := searchData{
		Query: store.SearchQuery{Q: strings.TrimSpace(q.Get("q")), Kind: q.Get("kind"), Team: q.Get("team"),
			API: q.Get("api"), Limit: searchLimit},
		Teams: s.teamOptions(),
		Kinds: searchKinds,
	}
	hits, err := s.Store.Search(r.Context(), d.Query)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	d.Hits, d.Truncated = len(hits), len(hits) == searchLimit
	d.Groups = groupHits(hits, d.Query, s.teamNames())
	if htmx(r) {
		w.Header().Set("HX-Push-Url", pushURL("/search", q))
		s.renderBlock(w, r, http.StatusOK, "search", "search-results", page{User: u, Data: d})
		return
	}
	title := "Search"
	if d.Query.Q != "" {
		title = d.Query.Q + " · Search"
	}
	s.render(w, r, http.StatusOK, "search", page{Title: title, Nav: "search", User: u, Data: d})
}

// groupHits groups hits by API, in the order of each API's best hit.
func groupHits(hits []store.Hit, sq store.SearchQuery, teams map[string]string) []searchGroup {
	var groups []searchGroup
	at := map[string]int{}
	for _, h := range hits {
		i, ok := at[h.APIID]
		if !ok {
			i = len(groups)
			at[h.APIID] = i
			groups = append(groups, searchGroup{APIID: h.APIID, APIKind: h.APIKind, Semver: h.Semver,
				Owner: cmp.Or(teams[h.Owner], h.Owner), Lifecycle: h.Lifecycle})
		}
		g := &groups[i]
		if h.Kind == "api" {
			g.Title, g.Snippet = h.Title, highlight(tidy(h.Snippet))
			continue
		}
		if sq.API == "" && len(g.Hits) == perGroup {
			g.More++
			continue
		}
		hit := searchHit{Kind: h.Kind, Ref: h.Ref, URL: hitURL(h)}
		// A snippet that marks nothing (a match on the title only) is just
		// the start of the indexed text.
		if strings.Contains(h.Snippet, store.SnippetStart) {
			hit.Snippet = highlight(tidy(h.Snippet))
		}
		g.Hits = append(g.Hits, hit)
	}
	for i := range groups {
		if groups[i].More > 0 {
			v := url.Values{"q": {sq.Q}, "api": {groups[i].APIID}}
			if sq.Kind != "" {
				v.Set("kind", sq.Kind)
			}
			groups[i].MoreURL = "/search?" + v.Encode()
		}
	}
	return groups
}

// hitURL is the page a hit opens. Operations and schemas of an OpenAPI
// have no page of their own yet, so they open its docs.
func hitURL(h store.Hit) string {
	version := "/apis/" + url.PathEscape(h.APIID) + "/versions/" + url.PathEscape(h.Semver)
	switch {
	case h.Kind == "message":
		return "/events/" + url.PathEscape(h.Ref)
	case h.APIKind == "openapi" && (h.Kind == "operation" || h.Kind == "schema"):
		return version + "/docs"
	}
	return version
}

// tidy collapses a snippet's spaces and repeated words: the index holds
// identifiers both as written and split ("status status", "orders orders"),
// which reads as noise. A marked repeat is kept over an unmarked one.
func tidy(s string) string {
	plain := func(w string) string {
		return strings.ToLower(strings.NewReplacer(store.SnippetStart, "", store.SnippetStop, "").Replace(w))
	}
	var out []string
	for _, w := range strings.Fields(s) {
		if n := len(out); n > 0 && plain(out[n-1]) == plain(w) {
			if strings.Contains(w, store.SnippetStart) {
				out[n-1] = w
			}
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// highlight escapes a snippet and marks its matched words. Postgres
// delimits them with control characters, so it never emits markup.
func highlight(s string) template.HTML {
	var b strings.Builder
	open := false
	for len(s) > 0 {
		i := strings.IndexAny(s, store.SnippetStart+store.SnippetStop)
		if i < 0 {
			b.WriteString(template.HTMLEscapeString(s))
			break
		}
		b.WriteString(template.HTMLEscapeString(s[:i]))
		switch {
		case s[i:i+1] == store.SnippetStart && !open:
			b.WriteString("<mark>")
			open = true
		case s[i:i+1] == store.SnippetStop && open:
			b.WriteString("</mark>")
			open = false
		}
		s = s[i+1:]
	}
	if open {
		b.WriteString("</mark>")
	}
	return template.HTML(b.String())
}

// pushURL is the address for an htmx response: path with only the
// parameters in use.
func pushURL(path string, q url.Values) string {
	clean := url.Values{}
	for k, vs := range q {
		if len(vs) > 0 && vs[0] != "" {
			clean.Set(k, vs[0])
		}
	}
	if len(clean) > 0 {
		path += "?" + clean.Encode()
	}
	return path
}
