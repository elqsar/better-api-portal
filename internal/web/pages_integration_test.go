//go:build integration

package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/check"
	"better-api-portal/internal/client"
	"better-api-portal/internal/config"
	"better-api-portal/internal/descriptor"
	"better-api-portal/internal/httpapi"
	"better-api-portal/internal/store"
	"better-api-portal/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

const example = "../../docs/spec/examples/orders-service"

// site is a portal with the example published, and the UI's server.
type site struct {
	t   *testing.T
	st  *store.Store
	ui  *httptest.Server
	api *client.Client
}

func newSite(t *testing.T) *site {
	t.Helper()
	cfg, err := config.Load(filepath.Join(example, "../portal.config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.PublicURL = "http://portal.test"
	cfg.OIDC = config.OIDC{Issuer: "http://idp.test", ClientID: "portal", GroupsClaim: "groups"}
	st := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	apiSrv := httptest.NewServer((&httpapi.Server{Store: st, Config: cfg, Auth: auth.NewCI(nil, st, nil), Log: log}).Handler())
	t.Cleanup(apiSrv.Close)
	tok, hash := auth.NewToken()
	if _, err := st.CreateToken(context.Background(), "acme/orders", hash, time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(apiSrv.URL, &client.Credentials{Token: tok})
	if err != nil {
		t.Fatal(err)
	}

	ui, err := New(Options{Store: st, Config: cfg, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	uiSrv := httptest.NewServer(ui.Handler())
	t.Cleanup(uiSrv.Close)
	return &site{t: t, st: st, ui: uiSrv, api: c}
}

// push publishes a copy of the example with edits (file, old, new) and
// returns each API's status.
func (s *site) push(edits ...[3]string) map[string]string {
	t := s.t
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	for _, e := range edits {
		p := filepath.Join(dir, e[0])
		b, _ := os.ReadFile(p)
		if !strings.Contains(string(b), e[1]) {
			t.Fatalf("%s: %q not found", e[0], e[1])
		}
		os.WriteFile(p, []byte(strings.Replace(string(b), e[1], e[2], 1)), 0o644)
	}
	d, _, err := descriptor.Load(filepath.Join(dir, "portal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bundles, _, err := check.BundleAPIs(d)
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := os.ReadFile(filepath.Join(dir, "portal.yaml"))
	resp, err := s.api.Push(context.Background(), desc, bundles, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range resp.APIs {
		out[a.ID] = a.Status
	}
	return out
}

// browser is signed in with the groups.
func (s *site) browser(groups ...string) *http.Client {
	s.t.Helper()
	id := randomString()
	err := s.st.CreateSession(context.Background(), hashID(id),
		store.Session{Subject: "u", Name: "Ada", Groups: groups, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		s.t.Fatal(err)
	}
	return &http.Client{
		Transport:     cookieTransport{id},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type cookieTransport struct{ id string }

func (c cookieTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.id})
	return http.DefaultTransport.RoundTrip(r)
}

func (s *site) get(c *http.Client, path string, headers ...string) (int, string, http.Header) {
	s.t.Helper()
	req, _ := http.NewRequest("GET", s.ui.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func contains(t *testing.T, what, body string, want ...string) {
	t.Helper()
	missing := false
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", what, w)
			missing = true
		}
	}
	if missing {
		t.Logf("%s:\n%s", what, body)
	}
}

func TestAPIList(t *testing.T) {
	s := newSite(t)
	s.push()
	c := s.browser()

	code, body, _ := s.get(c, "/apis")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	contains(t, "/apis", body, `href="/apis/orders-http/versions/2.3.0"`, `href="/apis/orders-events/versions/1.4.0"`,
		"<td>Orders</td>", `<span class="tag">checkout</span>`)

	code, body, h := s.get(c, "/apis?kind=openapi&team=&q=", "HX-Request", "true")
	if h.Get("HX-Push-Url") != "/apis?kind=openapi" {
		t.Errorf("pushed URL %q", h.Get("HX-Push-Url"))
	}
	if code != 200 || strings.Contains(body, "<html") || !strings.Contains(body, `id="api-table"`) {
		t.Fatalf("htmx: %d, want only the table:\n%s", code, body)
	}
	if strings.Contains(body, "orders-events") || !strings.Contains(body, "orders-http") {
		t.Errorf("kind filter:\n%s", body)
	}
	if _, body, _ := s.get(c, "/apis?tag=checkout&q=HTTP"); strings.Contains(body, "/apis/orders-events/") || !strings.Contains(body, "/apis/orders-http/") {
		t.Errorf("tag and q filters:\n%s", body)
	}
	if _, body, _ := s.get(c, "/apis?q=%25"); !strings.Contains(body, "No published API matches") {
		t.Errorf("q is a pattern:\n%s", body)
	}
}

func TestAPIPages(t *testing.T) {
	s := newSite(t)
	s.push()
	c := s.browser("eng-payments")

	code, _, h := s.get(c, "/apis/orders-http")
	if code != http.StatusFound || h.Get("Location") != "/apis/orders-http/versions/2.3.0" {
		t.Fatalf("latest: %d → %s", code, h.Get("Location"))
	}
	code, body, _ := s.get(c, "/apis/orders-http/versions/2.3.0")
	if code != 200 {
		t.Fatalf("overview: %d\n%s", code, body)
	}
	contains(t, "http overview", body, "<code>/orders/{orderId}/cancellation</code>", "method-POST",
		"Orders <span class=\"muted\">(team-orders)</span>", "Runbook", "https://orders.prod.internal",
		"Version <strong>2.3.0</strong> (latest)", "<dt>Lint score</dt>")

	_, body, _ = s.get(c, "/apis/orders-events/versions/1.4.0")
	contains(t, "events overview", body, `href="/events/com.acme.orders.order.created.v1"`, "kafka <code>orders.events</code>",
		`href="/apis/payments-events"`, "com.acme.payments.payment.captured.v1", "prod</strong> <span class=\"muted\">on kafka-prod")

	_, body, _ = s.get(c, "/apis/orders-http/versions/2.3.0/lint")
	contains(t, "lint", body, "sec-integer-bounds", "sev-warn", "first version the portal checked")

	// htmx tab switches get the tabs and panel only.
	_, body, _ = s.get(c, "/apis/orders-http/versions/2.3.0/lint", "HX-Request", "true")
	if strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), `<div id="api-body">`) {
		t.Errorf("htmx tab:\n%s", body)
	}

	for path, want := range map[string]int{
		"/apis/nope":                            404,
		"/apis/orders-http/versions/9.9.9":      404,
		"/apis/orders-http/versions/latest":     302,
		"/apis/orders-http/versions/9.9.9/lint": 404,
	} {
		if code, _, _ := s.get(c, path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

func TestAPIHistory(t *testing.T) {
	s := newSite(t)
	s.push()
	// A breaking change on a minor bump is rejected.
	st := s.push(
		[3]string{"api/openapi.yaml", "enum: [customer_request, payment_failed, out_of_stock]", "enum: [customer_request, payment_failed]"},
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.4.0"})
	if st["orders-http"] != "rejected" {
		t.Fatalf("statuses %v", st)
	}
	// A compatible change, now deprecated.
	st = s.push(
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.3.1"},
		[3]string{"portal.yaml", "    lifecycle: production\n    tags", "    lifecycle: deprecated\n    sunset: 2027-03-31\n    tags"})
	if st["orders-http"] != "published" {
		t.Fatalf("statuses %v", st)
	}

	owner, other := s.browser("eng-orders"), s.browser("eng-payments")
	_, body, _ := s.get(owner, "/apis/orders-http/versions")
	contains(t, "owner's versions", body, "2.4.0 <span class=\"badge sev-error\">rejected</span>", "1 error(s)",
		`href="/apis/orders-http/versions/2.3.1">2.3.1</a> <span class="badge">latest</span>`, "from 2.3.0")
	_, body, _ = s.get(other, "/apis/orders-http/versions")
	if strings.Contains(body, "2.4.0") {
		t.Error("another team sees a rejected push")
	}

	_, body, _ = s.get(other, "/apis/orders-http/versions/2.3.0")
	contains(t, "old version", body, "latest is 2.3.1", "Deprecated.", "retired on 2027-03-31")
	_, body, _ = s.get(other, "/apis/orders-http/versions/2.3.1/lint")
	contains(t, "lint with baseline", body, `Compared with <a href="/apis/orders-http/versions/2.3.0">2.3.0</a>`)
	_, body, _ = s.get(other, "/apis?lifecycle=deprecated")
	contains(t, "deprecated filter", body, "orders-http", "sunset 2027-03-31")
}
