package web

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"

	"better-api-portal/internal/model"
	"better-api-portal/internal/store"
)

// Lifecycles in the order the UI lists them (docs/spec/03-formats.md).
var lifecycles = []string{"experimental", "production", "deprecated", "retired"}

// Kinds the portal parses.
var kinds = []string{"openapi", "cloudevents", "asyncapi"}

type option struct{ Value, Label string }

type apiListData struct {
	Filter     store.APIFilter
	APIs       []store.APISummary
	Teams      []option
	Kinds      []string
	Lifecycles []string
	Tags       []string
	TeamNames  map[string]string
}

func (s *Server) teamNames() map[string]string {
	names := map[string]string{}
	for _, t := range s.Config.Teams {
		names[t.Slug] = cmp.Or(t.Name, t.Slug)
	}
	return names
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	f := store.APIFilter{Team: q.Get("team"), Kind: q.Get("kind"), Lifecycle: q.Get("lifecycle"), Tag: q.Get("tag"), Q: q.Get("q")}
	apis, err := s.Store.ListAPIs(r.Context(), f)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	d := apiListData{Filter: f, APIs: apis, Kinds: kinds, Lifecycles: lifecycles, TeamNames: s.teamNames()}
	for _, t := range s.Config.Teams {
		d.Teams = append(d.Teams, option{t.Slug, cmp.Or(t.Name, t.Slug)})
	}
	if htmx(r) {
		// The address bar gets only the filters in use.
		clean := url.Values{}
		for k, vs := range q {
			if len(vs) > 0 && vs[0] != "" {
				clean.Set(k, vs[0])
			}
		}
		push := "/apis"
		if len(clean) > 0 {
			push += "?" + clean.Encode()
		}
		w.Header().Set("HX-Push-Url", push)
		s.renderBlock(w, r, http.StatusOK, "apis", "api-table", page{User: u, Data: d})
		return
	}
	if d.Tags, err = s.Store.Tags(r.Context()); err != nil {
		s.fail(w, r, u, err)
		return
	}
	s.render(w, r, http.StatusOK, "apis", page{Title: "APIs", Nav: "apis", User: u, Data: d})
}

// apiData is what every tab of an API page gets.
type apiData struct {
	API       *store.APIDetail
	OwnerName string
	// Version is the version shown: the one in the URL, or the latest on
	// the versions tab. Nil if nothing is published.
	Version  *store.Version
	IsLatest bool
	Tab      string // overview, versions, lint
	// CanSeeRejected: owners and admins see rejected pushes.
	CanSeeRejected bool

	Spec      *model.Spec
	Report    *store.Report
	Consumes  []store.Dependency
	Consumers []store.Dependency
	Versions  []store.VersionSummary
	// Findings by severity, for the lint tab.
	Errors, Warnings, Infos int
}

// loadAPI loads the API and the version in the URL ("" for the latest).
// It writes the response itself and returns nil when the page can't be
// shown.
func (s *Server) loadAPI(w http.ResponseWriter, r *http.Request, u *User, semver string) *apiData {
	id := r.PathValue("id")
	api, err := s.Store.APIDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, u, err)
		return nil
	}
	if api == nil {
		s.error(w, r, u, http.StatusNotFound, "No such API", "No API has the id "+id+".")
		return nil
	}
	d := &apiData{API: api, OwnerName: cmp.Or(s.teamNames()[api.Owner], api.Owner),
		CanSeeRejected: u.Admin || slices.Contains(u.Teams, api.Owner)}
	if semver == "" {
		semver = api.LatestSemver
	}
	if semver != "" {
		v, err := s.Store.PublishedVersion(r.Context(), id, semver)
		if err != nil {
			s.fail(w, r, u, err)
			return nil
		}
		if v == nil {
			s.error(w, r, u, http.StatusNotFound, "No such version", id+" has no published version "+semver+".")
			return nil
		}
		d.Version, d.IsLatest = v, v.Semver == api.LatestSemver
	}
	return d
}

func (s *Server) renderAPI(w http.ResponseWriter, r *http.Request, u *User, d *apiData) {
	p := page{Title: d.API.ID, Nav: "apis", User: u, Data: d}
	if htmx(r) {
		s.renderBlock(w, r, http.StatusOK, "api", "api-body", p)
		return
	}
	s.render(w, r, http.StatusOK, "api", p)
}

// apiLatest sends /apis/{id} to the latest version.
func (s *Server) apiLatest(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadAPI(w, r, u, "")
	if d == nil {
		return
	}
	to := "/apis/" + url.PathEscape(d.API.ID) + "/versions"
	if d.Version != nil {
		to += "/" + url.PathEscape(d.Version.Semver)
	}
	http.Redirect(w, r, to, http.StatusFound)
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request, u *User) {
	if r.PathValue("version") == "latest" {
		s.apiLatest(w, r, u)
		return
	}
	d := s.loadAPI(w, r, u, r.PathValue("version"))
	if d == nil {
		return
	}
	d.Tab = "overview"
	var err error
	if d.Spec, err = s.Store.Model(r.Context(), d.Version.ID); err != nil {
		s.fail(w, r, u, err)
		return
	}
	if d.Report, err = s.Store.Report(r.Context(), d.Version.ID); err != nil {
		s.fail(w, r, u, err)
		return
	}
	if d.Consumes, d.Consumers, err = s.Store.Dependencies(r.Context(), d.API.ID); err != nil {
		s.fail(w, r, u, err)
		return
	}
	s.renderAPI(w, r, u, d)
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadAPI(w, r, u, "")
	if d == nil {
		return
	}
	d.Tab = "versions"
	vs, err := s.Store.Versions(r.Context(), d.API.ID)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	for _, v := range vs {
		if v.Status == store.StatusPublished || d.CanSeeRejected {
			d.Versions = append(d.Versions, v)
		}
	}
	s.renderAPI(w, r, u, d)
}

func (s *Server) apiLint(w http.ResponseWriter, r *http.Request, u *User) {
	d := s.loadAPI(w, r, u, r.PathValue("version"))
	if d == nil {
		return
	}
	d.Tab = "lint"
	var err error
	if d.Report, err = s.Store.Report(r.Context(), d.Version.ID); err != nil {
		s.fail(w, r, u, err)
		return
	}
	rank := map[model.Severity]int{model.SeverityError: 0, model.SeverityWarn: 1, model.SeverityInfo: 2}
	slices.SortStableFunc(d.Report.Findings, func(a, b model.Finding) int {
		return cmp.Or(cmp.Compare(rank[a.Severity], rank[b.Severity]), cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	c := model.Counts(d.Report.Findings)
	d.Errors, d.Warnings, d.Infos = c[model.SeverityError], c[model.SeverityWarn], c[model.SeverityInfo]
	s.renderAPI(w, r, u, d)
}
