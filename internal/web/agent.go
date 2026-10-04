package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/elqsar/better-api-portal/internal/agentdoc"
	"github.com/elqsar/better-api-portal/internal/store"
)

// Markdown for AI agents (internal/agentdoc): /llms.txt, /llms-full.txt,
// /search.md, and every API, operation and event page with .md appended or
// requested with Accept: text/markdown.

// agentDocVersion is part of a Markdown page's ETag: bump it when
// agentdoc's output changes.
const agentDocVersion = "1"

// portalName heads llms.txt, as it heads the web pages.
func (s *Server) portalName() string {
	return strings.TrimSpace(s.Config.Org.Name + " API portal")
}

// maxFull caps /llms-full.txt; the APIs past it are listed with links.
const maxFull = 512 << 10

// wantsMarkdown reports whether the request is for an agent's Markdown:
// a .md or llms*.txt path, or an Accept header that prefers text/markdown
// to text/html.
func wantsMarkdown(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasSuffix(p, ".md") || p == "/llms.txt" || p == "/llms-full.txt" {
		return true
	}
	accept := r.Header.Get("Accept")
	md := strings.Index(accept, "text/markdown")
	html := strings.Index(accept, "text/html")
	return md >= 0 && (html < 0 || md < html)
}

// trimMD strips .md from the path value name, for routes whose last
// segment may carry it.
func trimMD(r *http.Request, name string) {
	r.SetPathValue(name, strings.TrimSuffix(r.PathValue(name), ".md"))
}

// writeMarkdown sends a Markdown page. A non-empty etag makes it
// cacheable: versions are immutable.
func writeMarkdown(w http.ResponseWriter, r *http.Request, body, etag string) {
	h := w.Header()
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Add("Vary", "Accept")
	if etag != "" {
		etag = `"` + etag + "-md" + agentDocVersion + `"`
		h.Set("ETag", etag)
		h.Set("Cache-Control", "private, max-age=3600")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else {
		h.Set("Cache-Control", "private, no-cache")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write([]byte(body))
}

// markdownError is an error page for an agent.
func markdownError(w http.ResponseWriter, status int, heading, msg string) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte("# " + heading + "\n\n" + msg + "\n"))
}

func (s *Server) urls() agentdoc.URLs {
	if s.publicURL == nil {
		return agentdoc.URLs{}
	}
	return agentdoc.URLs{Base: strings.TrimSuffix(s.publicURL.String(), "/")}
}

// llmsTxt is the index: every API by team.
func (s *Server) llmsTxt(w http.ResponseWriter, r *http.Request, u *User) {
	apis, err := s.Store.ListAPIs(r.Context(), store.APIFilter{})
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	var entries []agentdoc.Entry
	for _, a := range apis {
		entries = append(entries, agentdoc.Entry{ID: a.ID, Kind: a.Kind, Title: a.Title, Owner: a.Owner,
			Lifecycle: a.Lifecycle, Version: a.Latest, Description: a.Description})
	}
	writeMarkdown(w, r, agentdoc.Catalogue(s.portalName(), entries, s.urls()), "")
}

// llmsFull concatenates the full pages of the APIs a team, tag or kind
// filter selects, up to maxFull.
func (s *Server) llmsFull(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	f := store.APIFilter{Team: q.Get("team"), Tag: q.Get("tag"), Kind: q.Get("kind")}
	apis, err := s.Store.ListAPIs(r.Context(), f)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	var b strings.Builder
	title := s.portalName()
	for _, part := range []string{f.Team, f.Tag, f.Kind} {
		if part != "" {
			title += ": " + part
		}
	}
	b.WriteString("# " + title + "\n")
	var skipped []store.APISummary
	for _, a := range apis {
		if a.Lifecycle == "retired" {
			continue
		}
		if b.Len() >= maxFull {
			skipped = append(skipped, a)
			continue
		}
		page, err := s.apiMarkdown(r.Context(), a.ID, a.Latest, true, 1)
		if err != nil {
			s.Log.Error("llms-full", "api", a.ID, "version", a.Latest, "err", err)
			b.WriteString("\n## " + a.ID + "\n\nThis API can't be shown: " + err.Error() + "\n")
			continue
		}
		b.WriteString("\n" + page)
	}
	if len(skipped) > 0 {
		b.WriteString("\n## Not included\n\nThis file stops at " + strconv.Itoa(maxFull>>10) +
			" KiB. Read these APIs one at a time, or narrow the file with ?team=, ?tag= or ?kind=:\n\n")
		for _, a := range skipped {
			b.WriteString("- [" + a.ID + "](" + s.urls().API(a.ID, "") + ")\n")
		}
	}
	writeMarkdown(w, r, b.String(), "")
}

// apiPageMarkdown serves an API's latest version, or the one in the URL.
func (s *Server) apiPageMarkdown(w http.ResponseWriter, r *http.Request, u *User, semver string) {
	d := s.loadAPI(w, r, u, semver)
	if d == nil {
		return
	}
	if d.Version == nil {
		markdownError(w, http.StatusNotFound, "Nothing published", d.API.ID+" has no published version yet.")
		return
	}
	page, err := s.apiMarkdown(r.Context(), d.API.ID, d.Version.Semver, false, 0)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	// The latest page's content changes with each push, so only a pinned
	// version gets an ETag.
	etag := ""
	if semver != "" {
		etag = d.Version.ContentHash
	}
	writeMarkdown(w, r, page, etag)
}

// apiMarkdown renders one version of an API.
func (s *Server) apiMarkdown(ctx context.Context, id, semver string, full bool, level int) (string, error) {
	a, v, err := s.agentAPI(ctx, id, semver)
	if err != nil {
		return "", err
	}
	consumes, consumers, err := s.Store.Dependencies(ctx, id)
	if err != nil {
		return "", err
	}
	a.Consumers = agentDeps(byRepo(consumers))
	for _, d := range consumes {
		i := slices.IndexFunc(a.Consumes, func(x agentdoc.Dependency) bool { return x.To == d.To })
		if i < 0 {
			a.Consumes = append(a.Consumes, agentdoc.Dependency{To: d.To, Types: d.Types})
		}
	}
	return agentdoc.APIPage(a, v, s.urls(), full, level), nil
}

// agentAPI loads what agentdoc needs about a published version.
func (s *Server) agentAPI(ctx context.Context, id, semver string) (*agentdoc.API, *agentdoc.Version, error) {
	api, err := s.Store.APIDetail(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if api == nil {
		return nil, nil, errNotFound
	}
	ver, err := s.Store.PublishedVersion(ctx, id, semver)
	if err != nil {
		return nil, nil, err
	}
	if ver == nil {
		return nil, nil, errNotFound
	}
	v, err := s.agentVersion(ctx, ver.ContentHash)
	if err != nil {
		return nil, nil, err
	}
	a := &agentdoc.API{ID: api.ID, Kind: api.Kind, Title: api.Title, Owner: api.Owner, Lifecycle: api.Lifecycle,
		System: api.System, Repo: api.Repo, Version: ver.Semver, Tags: api.Tags}
	if api.Sunset != nil {
		a.Sunset = api.Sunset.Format("2006-01-02")
	}
	for _, l := range api.Links {
		a.Links = append(a.Links, agentdoc.Link{Title: l.Title, URL: l.URL})
	}
	for _, e := range api.Environments {
		a.Environments = append(a.Environments, agentdoc.Environment{Name: e.Name, URL: e.URL, Broker: e.Broker})
	}
	return a, v, nil
}

var errNotFound = errors.New("not found")

// agentVersion parses a stored bundle with all its documents.
func (s *Server) agentVersion(ctx context.Context, hash string) (*agentdoc.Version, error) {
	if v, ok := s.agentVersions.get(hash); ok {
		return v, nil
	}
	b, err := s.unpack(ctx, hash)
	if err != nil {
		return nil, err
	}
	v, err := agentdoc.Load(b)
	if err != nil {
		return nil, err
	}
	s.agentVersions.put(hash, v)
	return v, nil
}

func agentDeps(repos []consumerRepo) []agentdoc.Dependency {
	var out []agentdoc.Dependency
	for _, c := range repos {
		out = append(out, agentdoc.Dependency{Repo: c.Repo, Owner: strings.Join(c.Owners, ", "),
			APIs: c.APIs, To: c.To, Types: c.Types})
	}
	return out
}

// operationMarkdown serves one operation, by operationId or by the
// method-and-path key that search results link to.
func (s *Server) operationMarkdown(w http.ResponseWriter, r *http.Request, u *User) {
	trimMD(r, "op")
	id, semver, key := r.PathValue("id"), r.PathValue("version"), r.PathValue("op")
	a, v, err := s.agentAPI(r.Context(), id, semver)
	if errors.Is(err, errNotFound) {
		markdownError(w, http.StatusNotFound, "No such version", id+" has no published version "+semver+".")
		return
	}
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	for _, op := range v.Spec.Operations {
		if agentdoc.OperationKey(op.Method, op.Path, op.OperationID) == key || agentdoc.OperationKey(op.Method, op.Path, "") == key {
			ver, err := s.Store.PublishedVersion(r.Context(), id, semver)
			if err != nil {
				s.fail(w, r, u, err)
				return
			}
			writeMarkdown(w, r, agentdoc.OperationPage(a, v, op, s.urls(), 0), ver.ContentHash+"-"+key)
			return
		}
	}
	markdownError(w, http.StatusNotFound, "No such operation",
		id+" "+semver+" has no operation "+key+". Its page lists them: "+s.urls().API(id, semver))
}

// eventMarkdown serves an event type: its owner's declaration and its
// consumers.
func (s *Server) eventMarkdown(w http.ResponseWriter, r *http.Request, u *User) {
	ctx := r.Context()
	typ := r.PathValue("type")
	declared, err := s.Store.MessageRoles(ctx, typ)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	var owners []string
	for _, m := range declared {
		owners = append(owners, m.APIID)
	}
	deps, err := s.Store.TypeConsumers(ctx, typ, owners)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	repos := byRepo(deps)
	for i := range repos {
		repos[i].Types = nil // they name this type
	}
	consumers := agentDeps(repos)
	if len(declared) == 0 {
		if len(consumers) == 0 {
			markdownError(w, http.StatusNotFound, "No such event type", "No API in the portal declares or consumes "+typ+".")
			return
		}
		var b strings.Builder
		b.WriteString("# `" + typ + "`\n\nNo API in the portal declares this type, so its contract isn't known. Consumed by:\n\n")
		for _, c := range consumers {
			b.WriteString("- " + c.Repo + " (" + strings.Join(c.APIs, ", ") + ")\n")
		}
		writeMarkdown(w, r, b.String(), "")
		return
	}
	owner := declared[0]
	a, v, err := s.agentAPI(ctx, owner.APIID, owner.Semver)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	for _, m := range v.Spec.Messages {
		if m.Key == typ {
			writeMarkdown(w, r, agentdoc.MessagePage(a, v, m, consumers, s.urls(), 0), "")
			return
		}
	}
	// The index and the model disagree; reindex fixes it.
	markdownError(w, http.StatusNotFound, "No such event type", owner.APIID+" "+owner.Semver+" doesn't declare "+typ+".")
}

// searchMarkdown lists search hits with links to their Markdown pages.
func (s *Server) searchMarkdown(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	sq := store.SearchQuery{Q: strings.TrimSpace(q.Get("q")), Kind: q.Get("kind"), Team: q.Get("team"),
		API: q.Get("api"), Limit: 50}
	if sq.Q == "" {
		markdownError(w, http.StatusBadRequest, "No query",
			"Pass the words to look for: /search.md?q=refund. Narrow with &kind=operation|message|schema|api, &team= or &api=.")
		return
	}
	hits, err := s.Store.Search(r.Context(), sq)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	writeMarkdown(w, r, agentdoc.SearchPage(sq.Q, agentHits(hits), len(hits) == sq.Limit, s.urls()), "")
}

func agentHits(hits []store.Hit) []agentdoc.Hit {
	var out []agentdoc.Hit
	for _, h := range hits {
		out = append(out, agentdoc.Hit{APIID: h.APIID, Version: h.Semver, Kind: h.Kind, Ref: h.Ref, Title: h.Title,
			APIKind: h.APIKind, Owner: h.Owner, Lifecycle: h.Lifecycle,
			Snippet: strings.NewReplacer(store.SnippetStart, "**", store.SnippetStop, "**").Replace(tidy(h.Snippet)),
			Marked:  strings.Contains(h.Snippet, store.SnippetStart)})
	}
	return out
}
