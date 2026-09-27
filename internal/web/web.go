// Package web is the portal's web UI (docs/spec/05-architecture.md §Web
// UI): server-rendered html/template pages behind OIDC sign-in, with htmx
// for partial updates. Templates and static files are embedded.
package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elqsar/better-api-portal/internal/config"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Store is what the UI reads and writes; *store.Store implements it.
type Store interface {
	CreateSession(ctx context.Context, idHash []byte, s store.Session) error
	Session(ctx context.Context, idHash []byte) (*store.Session, error)
	DeleteSession(ctx context.Context, idHash []byte) error
	RecentlyPublished(ctx context.Context, limit int) ([]store.Published, int, error)

	ListAPIs(ctx context.Context, f store.APIFilter) ([]store.APISummary, error)
	Tags(ctx context.Context) ([]string, error)
	APIDetail(ctx context.Context, id string) (*store.APIDetail, error)
	PublishedVersion(ctx context.Context, apiID, semver string) (*store.Version, error)
	Versions(ctx context.Context, apiID string) ([]store.VersionSummary, error)
	Model(ctx context.Context, versionID int64) (*model.Spec, error)
	Report(ctx context.Context, versionID int64) (*store.Report, error)
	Dependencies(ctx context.Context, apiID string) (consumes, consumers []store.Dependency, err error)
	MessageRoles(ctx context.Context, msgType string) ([]store.MessageRole, error)
	TypeConsumers(ctx context.Context, msgType string, owners []string) ([]store.Dependency, error)
	Search(ctx context.Context, q store.SearchQuery) ([]store.Hit, error)
	Bundle(ctx context.Context, contentHash string) ([]byte, error)
}

// Options configure a Server.
type Options struct {
	Store  Store
	Config *config.Config
	Log    *slog.Logger
	// ClientSecret is the OIDC client secret, if the provider needs one.
	ClientSecret string
	// HTTPClient reaches the identity provider; nil means
	// http.DefaultClient.
	HTTPClient *http.Client
}

// Server serves the web UI.
type Server struct {
	Options
	pages      map[string]*template.Template
	static     map[string]staticFile
	publicURL  *url.URL
	secure     bool // cookies need https
	loginReady bool // an issuer is configured

	mu       sync.Mutex
	provider *oidcProvider

	docs  cache[[]byte]       // resolved OpenAPI documents
	specs cache[*model.Spec]  // bundles parsed with their schemas, for event pages
	diffs cache[*versionDiff] // by the two content hashes and the compatibility mode
}

type staticFile struct {
	data  []byte
	gz    []byte // gzipped, if that is smaller
	ctype string
	hash  string
}

// New prepares the templates and static files.
func New(o Options) (*Server, error) {
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	s := &Server{Options: o, pages: map[string]*template.Template{}, static: map[string]staticFile{}}
	if o.Config.OIDC.Issuer != "" {
		u, err := url.Parse(o.Config.Server.PublicURL)
		if err != nil || u.Host == "" {
			return nil, errors.New("server.publicURL is required for sign-in: it is where the identity provider sends users back")
		}
		s.publicURL, s.secure, s.loginReady = u, u.Scheme == "https", true
	}

	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		name := strings.TrimPrefix(p, "static/")
		f := staticFile{data: b, ctype: mime.TypeByExtension(path.Ext(name)), hash: hex.EncodeToString(sum[:6])}
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		zw.Write(b)
		zw.Close()
		if gz.Len() < len(b)*9/10 {
			f.gz = gz.Bytes()
		}
		s.static[name] = f
		return nil
	})
	if err != nil {
		return nil, err
	}

	funcs := template.FuncMap{
		"date": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
		"day": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Format("2006-01-02")
		},
		// short abbreviates a hash or commit.
		"short": func(s string, n int) string {
			s = strings.TrimPrefix(s, "sha256:")
			if len(s) > n {
				return s[:n]
			}
			return s
		},
		"diffURL": diffURL,
		"foldURL": foldURL,
		// lineNo is a diff line number, blank on the side the line isn't on.
		"lineNo": func(n int) string {
			if n == 0 {
				return ""
			}
			return strconv.Itoa(n)
		},
		"static": func(name string) (string, error) {
			f, ok := s.static[name]
			if !ok {
				return "", errors.New("no static file " + name)
			}
			return "/static/" + name + "?v=" + f.hash, nil
		},
	}
	// Every page gets the layout and the partials, _*.html.
	layout, err := template.New("layout").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/_*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		name := strings.TrimSuffix(path.Base(p), ".html")
		if name == "layout" || strings.HasPrefix(name, "_") {
			continue
		}
		t, err := template.Must(layout.Clone()).ParseFS(templateFS, p)
		if err != nil {
			return nil, err
		}
		s.pages[name] = t
	}
	return s, nil
}

// Handler returns the UI's routes. Everything but static files and the
// sign-in flow needs a session.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/{file}", s.serveStatic)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /logout", s.logout)
	mux.Handle("GET /{$}", s.authed(s.home))
	mux.Handle("GET /search", s.authed(s.search))
	mux.Handle("GET /apis", s.authed(s.apiList))
	mux.Handle("GET /apis/{id}", s.authed(s.apiLatest))
	mux.Handle("GET /apis/{id}/versions", s.authed(s.apiVersions))
	mux.Handle("GET /apis/{id}/diff", s.authed(s.apiDiff))
	mux.Handle("GET /apis/{id}/diff/lines", s.authed(s.diffLines))
	mux.Handle("GET /apis/{id}/versions/{version}", s.authed(s.apiOverview))
	mux.Handle("GET /apis/{id}/versions/{version}/lint", s.authed(s.apiLint))
	mux.Handle("GET /apis/{id}/versions/{version}/docs", s.authed(s.apiDocs))
	mux.Handle("GET /apis/{id}/versions/{version}/openapi.json", s.authed(s.openAPIDocument))
	mux.Handle("GET /events/{type}", s.authed(s.event))
	mux.Handle("/", s.authed(func(w http.ResponseWriter, r *http.Request, u *User) {
		s.error(w, r, u, http.StatusNotFound, "Not found", "There is no page at "+r.URL.Path+".")
	}))
	return securityHeaders(sameOrigin(mux))
}

// securityHeaders sets a strict CSP: scripts and styles only from this
// origin, so no inline script, style or eval.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp())
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

// csp is the Content-Security-Policy, with extra style sources for pages
// that need them.
func csp(styleSrc ...string) string {
	return "default-src 'self'; script-src 'self'; style-src " + strings.Join(append([]string{"'self'"}, styleSrc...), " ") +
		"; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
}

// sameOrigin refuses state-changing requests from other sites. The
// session cookie is SameSite=Lax already; this is the second line.
func sameOrigin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", http.StatusForbidden)
					return
				}
			} else if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	f, ok := s.static[r.PathValue("file")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("Vary", "Accept-Encoding")
	if r.URL.Query().Get("v") == f.hash {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", `"`+f.hash+`"`)
		if r.Header.Get("If-None-Match") == `"`+f.hash+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if f.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		w.Write(f.gz)
		return
	}
	w.Write(f.data)
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.TrimSpace(enc) == "gzip" && strings.TrimSpace(q) != "q=0" {
			return true
		}
	}
	return false
}

// page is what every template gets.
type page struct {
	Title string
	Org   string
	Nav   string // the current top-level section
	User  *User
	Data  any
	// Wide pages use the whole window.
	Wide bool
	// StyleNonce lets a script-injected stylesheet through the CSP (Scalar's).
	StyleNonce string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	s.renderBlock(w, r, status, name, "layout", p)
}

// renderBlock renders one template of a page: "layout" for the whole page,
// or a fragment for an htmx request.
func (s *Server) renderBlock(w http.ResponseWriter, r *http.Request, status int, name, block string, p page) {
	p.Org = s.Config.Org.Name
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, block, p); err != nil {
		s.Log.Error("render", "page", name, "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Add("Vary", "HX-Request")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// htmx says whether the request is htmx's, which wants a fragment. A
// history-restoring request wants the whole page.
func htmx(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

type errorData struct {
	Status           int
	Heading, Message string
}

func (s *Server) error(w http.ResponseWriter, r *http.Request, u *User, status int, heading, msg string) {
	s.render(w, r, status, "error", page{Title: heading, User: u, Data: errorData{status, heading, msg}})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, u *User, err error) {
	s.Log.Error("request failed", "path", r.URL.Path, "err", err)
	s.error(w, r, u, http.StatusInternalServerError, "Something went wrong",
		"The portal couldn't show this page. Try again; if it keeps happening, tell the platform team.")
}

type homeData struct {
	APIs   int
	Recent []store.Published
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, u *User) {
	recent, n, err := s.Store.RecentlyPublished(r.Context(), 20)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	s.render(w, r, http.StatusOK, "home", page{Nav: "home", User: u, Data: homeData{APIs: n, Recent: recent}})
}

// User is the signed-in user, with the roles their groups give them.
type User struct {
	store.Session
	Teams []string // slugs of the teams whose group they are in: they own those teams' APIs
	Admin bool
}

// DisplayName is the name to show.
func (u *User) DisplayName() string {
	for _, s := range []string{u.Name, u.Email, u.Subject} {
		if s != "" {
			return s
		}
	}
	return "?"
}

func (s *Server) user(sess *store.Session) *User {
	u := &User{Session: *sess}
	for _, t := range s.Config.Teams {
		if t.OIDCGroup != "" && slices.Contains(sess.Groups, t.OIDCGroup) {
			u.Teams = append(u.Teams, t.Slug)
		}
	}
	if g := s.Config.Admins.OIDCGroup; g != "" && slices.Contains(sess.Groups, g) {
		u.Admin = true
	}
	return u
}
