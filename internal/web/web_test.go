package web

import (
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"better-api-portal/internal/config"
	"better-api-portal/internal/store"
	"better-api-portal/internal/web/devoidc"
)

// memStore keeps sessions in memory.
type memStore struct {
	mu       sync.Mutex
	sessions map[string]store.Session
	recent   []store.Published
}

func (m *memStore) CreateSession(_ context.Context, h []byte, s store.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(h)] = s
	return nil
}

func (m *memStore) Session(_ context.Context, h []byte) (*store.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok || time.Now().After(s.ExpiresAt) {
		return nil, nil
	}
	return &s, nil
}

func (m *memStore) DeleteSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(h))
	return nil
}

func (m *memStore) RecentlyPublished(context.Context, int) ([]store.Published, int, error) {
	return m.recent, len(m.recent), nil
}

func testConfig() *config.Config {
	return &config.Config{
		Org:    config.Org{Name: "Acme"},
		Teams:  []config.Team{{Slug: "team-orders", OIDCGroup: "eng-orders"}, {Slug: "team-payments", OIDCGroup: "eng-payments"}},
		Admins: config.Admins{OIDCGroup: "eng-platform"},
	}
}

// portal starts the UI with the stub provider mounted, as serve --dev-login
// does.
func portal(t *testing.T, st *memStore) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	cfg := testConfig()
	cfg.Server.PublicURL = base
	idp, err := devoidc.New(base+"/dev/oidc", []string{"eng-orders", "eng-platform"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.OIDC = config.OIDC{Issuer: idp.Issuer(), ClientID: devoidc.ClientID, GroupsClaim: "groups"}
	s, err := New(Options{Store: st, Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/dev/oidc/", idp.Handler())
	mux.Handle("/", s.Handler())
	srv.Config.Handler = mux
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

var hiddenField = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`)

// signIn submits the stub's sign-in form on page, with only the groups
// given, and returns where the browser ends up.
func signIn(t *testing.T, c *http.Client, srv *httptest.Server, page string, groups ...string) (*http.Response, string) {
	t.Helper()
	if !strings.Contains(page, "Development sign-in") {
		t.Fatalf("not the sign-in page:\n%s", page)
	}
	form := url.Values{"name": {"Ada Lovelace"}, "email": {"ada@example.com"}, "groups": groups}
	for _, m := range hiddenField.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	resp, err := c.PostForm(srv.URL+"/dev/oidc/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSignInAndOut(t *testing.T) {
	st := &memStore{sessions: map[string]store.Session{},
		recent: []store.Published{{APIID: "orders-http", Kind: "openapi", Semver: "2.3.0", PublishedAt: time.Now()}}}
	srv := portal(t, st)
	c := browser(t)

	_, page := get(t, c, srv.URL+"/")
	resp, body := signIn(t, c, srv, page, "eng-platform")
	if resp.StatusCode != 200 || resp.Request.URL.Path != "/" {
		t.Fatalf("after sign-in: %d at %s", resp.StatusCode, resp.Request.URL)
	}
	for _, want := range []string{"Ada Lovelace", `class="badge admin"`, `href="/apis/orders-http/versions/2.3.0"`, "Acme API portal"} {
		if !strings.Contains(body, want) {
			t.Errorf("home lacks %q:\n%s", want, body)
		}
	}
	if len(st.sessions) != 1 {
		t.Fatalf("%d sessions", len(st.sessions))
	}
	for _, s := range st.sessions {
		if s.Subject != "dev:ada.lovelace" || !slices.Equal(s.Groups, []string{"eng-platform"}) || s.Email != "ada@example.com" {
			t.Errorf("session = %+v", s)
		}
	}

	req, _ := http.NewRequest("POST", srv.URL+"/logout", nil)
	req.Header.Set("Origin", srv.URL)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(st.sessions) != 0 || !strings.Contains(string(b), "Development sign-in") {
		t.Errorf("after sign-out: %d session(s), page:\n%s", len(st.sessions), b)
	}
}

func TestSignInReturnsToPage(t *testing.T) {
	srv := portal(t, &memStore{sessions: map[string]store.Session{}})
	c := browser(t)
	_, page := get(t, c, srv.URL+"/nowhere?x=1")
	resp, body := signIn(t, c, srv, page)
	if resp.StatusCode != 404 || resp.Request.URL.RequestURI() != "/nowhere?x=1" || !strings.Contains(body, "There is no page at /nowhere") {
		t.Errorf("%d at %s:\n%s", resp.StatusCode, resp.Request.URL, body)
	}
	if strings.Contains(body, "badge admin") {
		t.Error("a user without the admins group is an admin")
	}
}

func TestCallbackRefusesForgedState(t *testing.T) {
	srv := portal(t, &memStore{sessions: map[string]store.Session{}})
	c := browser(t)
	get(t, c, srv.URL+"/login") // sets the login cookie, then shows the sign-in page
	resp, _ := get(t, c, srv.URL+"/auth/callback?code=x&state=forged")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d", resp.StatusCode)
	}
	resp, _ = get(t, browser(t), srv.URL+"/auth/callback?code=x&state=y")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("without a sign-in in progress: status %d", resp.StatusCode)
	}
}

func TestCrossOriginPostRefused(t *testing.T) {
	srv := portal(t, &memStore{sessions: map[string]store.Session{}})
	req, _ := http.NewRequest("POST", srv.URL+"/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	srv := portal(t, &memStore{sessions: map[string]store.Session{}})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := get(t, c, srv.URL+"/")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2F" {
		t.Errorf("unauthenticated: %d → %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	resp, body := get(t, c, srv.URL+"/login")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	cookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Errorf("login cookie = %q", cookie)
	}

	s, _ := New(Options{Store: &memStore{}, Config: testConfig(), Log: slog.Default()})
	css := s.static["app.css"]
	resp, _ = get(t, c, srv.URL+"/static/app.css?v="+css.hash)
	if resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || resp.Header.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("versioned static: %v", resp.Header)
	}
	if resp, _ := get(t, c, srv.URL+"/static/app.css"); resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("unversioned static: %v", resp.Header)
	}
	if resp, _ := get(t, c, srv.URL+"/static/nope.js"); resp.StatusCode != 404 {
		t.Errorf("missing static: %d", resp.StatusCode)
	}
}

func TestNotConfigured(t *testing.T) {
	s, err := New(Options{Store: &memStore{}, Config: testConfig(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, body := get(t, http.DefaultClient, srv.URL+"/")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "oidc.issuer") {
		t.Errorf("%d:\n%s", resp.StatusCode, body)
	}
	cfg := testConfig()
	cfg.OIDC = config.OIDC{Issuer: "https://login.example", ClientID: "portal"}
	if _, err := New(Options{Store: &memStore{}, Config: cfg}); err == nil || !strings.Contains(err.Error(), "publicURL") {
		t.Errorf("no public URL: %v", err)
	}
}

func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/apis/x?y=1":          "/apis/x?y=1",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example":       "/",
		`/\evil.example`:       "/",
	} {
		if got := localPath(in); got != want {
			t.Errorf("localPath(%q) = %q", in, got)
		}
	}
}

func TestUserRoles(t *testing.T) {
	s := &Server{Options: Options{Config: testConfig()}}
	u := s.user(&store.Session{Subject: "x", Groups: []string{"eng-payments", "eng-orders", "other"}})
	if !slices.Equal(u.Teams, []string{"team-orders", "team-payments"}) || u.Admin || u.DisplayName() != "x" {
		t.Errorf("user = %+v", u)
	}
}

// A browser fetches /favicon.ico and other subresources alongside a page;
// they mustn't start sign-ins of their own, and parallel sign-ins both
// complete.
func TestParallelSignIns(t *testing.T) {
	srv := portal(t, &memStore{sessions: map[string]store.Session{}})
	c := browser(t)
	_, first := get(t, c, srv.URL+"/")
	_, second := get(t, c, srv.URL+"/other") // another tab
	req, _ := http.NewRequest("GET", srv.URL+"/whatever.png", nil)
	req.Header.Set("Sec-Fetch-Dest", "image")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("subresource: %d", resp.StatusCode)
	}
	if resp, _ := signIn(t, c, srv, first); resp.StatusCode != 200 || resp.Request.URL.Path != "/" {
		t.Errorf("first sign-in: %d at %s", resp.StatusCode, resp.Request.URL)
	}
	if resp, _ := signIn(t, c, srv, second); resp.StatusCode != 404 || resp.Request.URL.Path != "/other" {
		t.Errorf("second sign-in: %d at %s", resp.StatusCode, resp.Request.URL)
	}
}
