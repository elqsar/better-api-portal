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
	"regexp"
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

func TestDocs(t *testing.T) {
	s := newSite(t)
	s.push()
	c := s.browser()

	code, body, h := s.get(c, "/apis/orders-http/versions/2.3.0/docs")
	if code != 200 {
		t.Fatalf("docs: %d", code)
	}
	contains(t, "docs", body, `data-url="/apis/orders-http/versions/2.3.0/openapi.json"`, "/static/scalar.js?v=", "/static/docs.js?v=",
		`aria-current="page">Docs</a>`)
	// Scalar's stylesheet gets through by the page's nonce, and only it.
	m := regexp.MustCompile(`<meta property="csp-nonce" content="([^"]+)">`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(h.Get("Content-Security-Policy"), "style-src 'self' 'nonce-"+m[1]+"'") ||
		!strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self';") {
		t.Errorf("nonce %v, CSP %q", m, h.Get("Content-Security-Policy"))
	}
	if _, other, _ := s.get(c, "/apis/orders-http/versions/2.3.0"); strings.Contains(other, "csp-nonce") {
		t.Error("a page without Scalar has a style nonce")
	}

	code, doc, h := s.get(c, "/apis/orders-http/versions/2.3.0/openapi.json")
	if code != 200 || h.Get("Content-Type") != "application/json" || !strings.Contains(doc, `"openapi":"3.1.0"`) ||
		!strings.Contains(doc, `"/orders/{orderId}/cancellation"`) {
		t.Fatalf("document: %d %s\n%s", code, h.Get("Content-Type"), doc)
	}
	if code, _, _ := s.get(c, "/apis/orders-http/versions/2.3.0/openapi.json", "If-None-Match", h.Get("ETag")); code != http.StatusNotModified {
		t.Errorf("revalidation: %d", code)
	}
	for path, want := range map[string]int{
		"/apis/orders-events/versions/1.4.0/docs":         404,
		"/apis/orders-events/versions/1.4.0/openapi.json": 404,
	} {
		if code, _, _ := s.get(c, path); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	// Without a session the document isn't served, and a fetch isn't sent
	// to sign in.
	if code, _, _ := s.get(http.DefaultClient, "/apis/orders-http/versions/2.3.0/openapi.json", "Sec-Fetch-Dest", "empty"); code != 401 {
		t.Errorf("anonymous document: %d", code)
	}
}

func TestEventPage(t *testing.T) {
	s := newSite(t)
	s.push()
	c := s.browser()

	code, body, _ := s.get(c, "/events/com.acme.orders.order.created.v1")
	if code != 200 {
		t.Fatalf("event: %d\n%s", code, body)
	}
	contains(t, "event", body,
		`Produced by
    <a href="/apis/orders-events/versions/1.4.0">orders-events</a> 1.4.0`,
		"An order was placed.", "<dt>source</dt><dd><code>/orders-service/{region}</code>", "<code>partitionkey</code>",
		// The payload tree, with the $ref to another file resolved.
		`<span class="s-name">total</span>`, "→ ./money.json", `<span class="s-name">amount</span>`, "Minor units, e.g. cents.",
		`<span class="s-name">quantity</span>`, "≥ 1",
		// Examples, bindings and the brokers of the API's environments.
		"ord_123", "<code>orders.events</code>", "mode:</span> <code>binary</code>",
		`<strong>prod</strong> <a href="https://kafka-ui.internal/prod" rel="noopener">kafka-prod</a>`,
		"<strong>staging</strong> kafka-staging")
	if strings.Contains(body, "banner") {
		t.Errorf("unexpected banner:\n%s", body)
	}

	// Inline schemas and receives.
	_, body, _ = s.get(c, "/events/com.acme.orders.order.cancelled.v1")
	contains(t, "inline schema", body, `<span class="s-name">reason</span>`, `&#34;payment_failed&#34;`)
	_, body, _ = s.get(c, "/events/com.acme.orders.order.cancel.requested.v1")
	contains(t, "receives", body, "Received by", "<code>orders.commands.cancel</code>")

	// A type the portal only knows as consumed.
	code, body, _ = s.get(c, "/events/com.acme.payments.payment.captured.v1")
	if code != 200 {
		t.Fatalf("consumed-only: %d", code)
	}
	contains(t, "consumed-only", body, "No producer published", `<a href="/apis/orders-events">orders-events</a>`,
		`<a href="/apis/orders-http">orders-http</a>`)

	if code, _, _ := s.get(c, "/events/com.acme.nope.v1"); code != 404 {
		t.Errorf("unknown type: %d", code)
	}

	// Deprecated, consumed as part of the whole API, and declared twice.
	st := s.push(
		[3]string{"api/events.yaml", "version: 1.4.0", "version: 1.5.0"},
		[3]string{"api/events.yaml", "    summary: An order was placed.", "    summary: An order was placed.\n    deprecated: true"},
		[3]string{"portal.yaml", "consumes:\n", "consumes:\n  - api: orders-events\n"},
		[3]string{"portal.yaml", "consumes:", `  - id: orders-events-copy
    kind: cloudevents
    spec: api/events.yaml
    lifecycle: production

consumes:`})
	if st["orders-events"] != "published" || st["orders-events-copy"] != "published" {
		t.Fatalf("statuses %v", st)
	}
	_, body, _ = s.get(c, "/events/com.acme.orders.order.created.v1")
	contains(t, "changed event", body, "Deprecated.", "orders-events</a> 1.5.0",
		// orders-http's push is unchanged, which doesn't update its consumes.
		`<a href="/apis/orders-events-copy">orders-events-copy</a> <span class="muted">(everything from orders-events)</span>`,
		"Declared by more than one API.", `<a href="/apis/orders-events-copy/versions/1.5.0">orders-events-copy</a> (produces)`)
}

func TestSearchPage(t *testing.T) {
	s := newSite(t)
	s.push([3]string{"api/events.yaml", "summary: An order was cancelled.", "summary: An order was <b>cancelled</b> & closed."})
	c := s.browser()

	code, body, _ := s.get(c, "/search?q=cancel")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	contains(t, "/search?q=cancel", body,
		`<a href="/apis/orders-events/versions/`, `<a href="/apis/orders-http/versions/2.3.0">orders-http</a>`,
		`href="/events/com.acme.orders.order.cancelled.v1"`,
		`href="/apis/orders-http/versions/2.3.0/docs"><code>POST /orders/{orderId}/cancellation</code>`,
		`An order was <mark>cancelled</mark> &amp; closed.`, // escaped, marked, tags dropped by ts_headline
		`<title>cancel · Search`)
	if strings.Contains(body, `class="topbar-search"`) {
		t.Error("the search page has a second search box in the top bar")
	}
	if strings.Contains(body, "status status") || strings.Contains(body, "reason reason") {
		t.Errorf("untidy or unmarked snippets:\n%s", body)
	}

	code, body, h := s.get(c, "/search?q=cancel&kind=message&team=", "HX-Request", "true")
	if h.Get("HX-Push-Url") != "/search?kind=message&q=cancel" {
		t.Errorf("pushed URL %q", h.Get("HX-Push-Url"))
	}
	if code != 200 || strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), `<div id="search-results">`) {
		t.Fatalf("htmx: %d, want only the results:\n%s", code, body)
	}
	if strings.Contains(body, "orders-http") {
		t.Errorf("kind filter:\n%s", body)
	}

	_, body, _ = s.get(c, "/search")
	contains(t, "/search", body, `name="q" value=""`, "Search every published API")
	if strings.Contains(body, "result-group") {
		t.Errorf("results without a query:\n%s", body)
	}
	_, body, _ = s.get(c, "/search?q=zzzzqqq")
	contains(t, "no match", body, "Nothing matches <strong>zzzzqqq</strong>")

	// Other pages get a search box in the top bar.
	_, body, _ = s.get(c, "/apis")
	contains(t, "/apis", body, `class="topbar-search" action="/search"`)
}
