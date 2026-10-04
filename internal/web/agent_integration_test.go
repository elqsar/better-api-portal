//go:build integration

package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elqsar/better-api-portal/internal/agentmcp"
	"github.com/elqsar/better-api-portal/internal/store"
)

// mdGet fetches a Markdown page and checks its status and type.
func (s *site) mdGet(c *http.Client, path string, status int, headers ...string) (string, http.Header) {
	s.t.Helper()
	code, body, h := s.get(c, path, headers...)
	if code != status {
		s.t.Fatalf("%s: status %d, want %d\n%s", path, code, status, body)
	}
	if ct := h.Get("Content-Type"); code != http.StatusNotModified && ct != "text/markdown; charset=utf-8" {
		s.t.Fatalf("%s: Content-Type %q\n%s", path, ct, body)
	}
	return body, h
}

func TestAgentMarkdown(t *testing.T) {
	s := catalogue(t)
	c := s.browser()

	body, _ := s.mdGet(c, "/llms.txt", 200)
	contains(t, "llms.txt", body, "# Acme API portal",
		"- [Orders API](http://portal.test/apis/orders-http.md): `orders-http`, OpenAPI, 2.3.0. Create, read and cancel orders.",
		"## team-payments", "http://portal.test/search.md?q={words}",
		"[Every API of team-orders in one file](http://portal.test/llms-full.txt?team=team-orders)")

	// The latest version, by .md or by Accept; the HTML page is unchanged.
	latest, _ := s.mdGet(c, "/apis/orders-http.md", 200)
	contains(t, "api page", latest, "# Orders API", "## Operations",
		"(http://portal.test/apis/orders-http/versions/2.3.0/operations/getOrder.md): Get an order")
	if accepted, _ := s.mdGet(c, "/apis/orders-http/versions/latest", 200, "Accept", "text/markdown, */*"); accepted != latest {
		t.Errorf("Accept: text/markdown gives\n%s\nwant\n%s", accepted, latest)
	}
	if code, body, _ := s.get(c, "/apis/orders-http/versions/2.3.0", "Accept", "text/html,text/markdown"); code != 200 || !strings.Contains(body, "<html") {
		t.Errorf("a browser's Accept gets HTML: %d\n%s", code, body)
	}

	// A pinned version is cacheable.
	_, h := s.mdGet(c, "/apis/orders-http/versions/2.3.0.md", 200)
	if h.Get("ETag") == "" {
		t.Fatal("no ETag on a pinned version")
	}
	s.mdGet(c, "/apis/orders-http/versions/2.3.0.md", http.StatusNotModified, "If-None-Match", h.Get("ETag"))

	// Operations by operationId, and by the method-and-path key search uses.
	op, _ := s.mdGet(c, "/apis/orders-http/versions/2.3.0/operations/getOrder.md", 200)
	contains(t, "operation", op, "# `GET /orders/{orderId}`: Get an order", "### 401, 404: Error",
		"- `total` (object, required, Money)")
	if byPath, _ := s.mdGet(c, "/apis/orders-http/versions/2.3.0/operations/get-orders-orderId.md", 200); byPath != op {
		t.Errorf("by method and path:\n%s", byPath)
	}

	ev, _ := s.mdGet(c, "/events/com.acme.orders.order.created.v1.md", 200)
	contains(t, "event", ev, "# `com.acme.orders.order.created.v1`", "## Payload (data)", "- `total`",
		"## Consumers", "- acme/payments (payments-events, payments-http), team-payments\n")

	found, _ := s.mdGet(c, "/search.md?q=refund", 200)
	contains(t, "search", found, "# Search: refund", "http://portal.test/events/com.acme.payments.refund.issued.v1.md")

	full, _ := s.mdGet(c, "/llms-full.txt?team=team-payments", 200)
	contains(t, "llms-full", full, "# Acme API portal: team-payments", "\n## Payment events\n",
		"\n### `com.acme.payments.refund.issued.v1`\n", "\n### `POST /orders/{orderId}/refunds`")
	if strings.Contains(full, "orders-http") && strings.Contains(full, "\n## Orders API") {
		t.Error("llms-full for one team has another's API")
	}

	// Errors are Markdown too, and an agent without a session gets 401,
	// not a sign-in redirect.
	notFound, _ := s.mdGet(c, "/apis/nope.md", 404)
	contains(t, "404", notFound, "# No such API")
	s.mdGet(c, "/apis/orders-http/versions/2.3.0/operations/nope.md", 404)
	s.mdGet(c, "/search.md", 400)
	s.mdGet(&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		"/llms.txt", 401)
}

// post sends a form, as a page in the portal would.
func (s *site) post(c *http.Client, path string, form url.Values, headers ...string) (int, string) {
	s.t.Helper()
	req, _ := http.NewRequest("POST", s.ui.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", s.ui.URL)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var (
	patRE    = regexp.MustCompile(`pat_[A-Za-z0-9_-]{43}`)
	revokeRE = regexp.MustCompile(`action="/tokens/(\d+)/revoke"`)
)

func TestPersonalAccessTokens(t *testing.T) {
	s := newSite(t)
	s.push()
	c := s.browser()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	if _, body, _ := s.get(c, "/tokens"); !strings.Contains(body, "You have no tokens.") {
		t.Fatalf("tokens page:\n%s", body)
	}
	if code, body := s.post(c, "/tokens", url.Values{"label": {""}, "days": {"90"}}); code != 400 || !strings.Contains(body, "Give the token a name") {
		t.Errorf("no label: %d\n%s", code, body)
	}
	if code, _ := s.post(c, "/tokens", url.Values{"label": {"x"}, "days": {"7"}}); code != 400 {
		t.Errorf("an expiry not offered: %d", code)
	}
	code, body := s.post(c, "/tokens", url.Values{"label": {"Claude Code"}, "days": {"90"}})
	tok := patRE.FindString(body)
	if code != 200 || tok == "" {
		t.Fatalf("create: %d\n%s", code, body)
	}
	contains(t, "new token", body, "Your new token: Claude Code", "/llms.txt</code>")

	// The token reads, as Markdown or HTML, but only reads.
	auth := []string{"Authorization", "Bearer " + tok}
	s.mdGet(noRedirect, "/llms.txt", 200, auth...)
	if code, body, _ := s.get(noRedirect, "/apis/orders-http/versions/2.3.0", auth...); code != 200 || !strings.Contains(body, "<html") {
		t.Errorf("an HTML page with a token: %d", code)
	}
	if code, _ := s.post(noRedirect, "/tokens", url.Values{"label": {"more"}, "days": {"90"}}, auth...); code != 403 {
		t.Errorf("POST with a token: %d", code)
	}
	if code, _, _ := s.get(noRedirect, "/tokens", auth...); code != 403 {
		t.Errorf("tokens page with a token: %d", code)
	}

	// The list shows the token, now used, and never the token itself.
	_, page, _ := s.get(c, "/tokens")
	m := revokeRE.FindStringSubmatch(page)
	if m == nil || strings.Contains(page, tok) || strings.Contains(page, ">never<") {
		t.Fatalf("tokens page after use:\n%s", page)
	}

	for _, bad := range []string{"pat_nope", "ptk_ci-token"} {
		s.mdGet(noRedirect, "/llms.txt", 401, "Authorization", "Bearer "+bad)
	}
	old := "pat_" + randomString()
	if _, err := s.st.CreateUserToken(context.Background(), hashID(old), store.Session{Subject: "u"}, "old",
		time.Now().Add(-24*time.Hour)); err != nil { // a day: the database's clock may be off from ours
		t.Fatal(err)
	}
	s.mdGet(noRedirect, "/llms.txt", 401, "Authorization", "Bearer "+old)

	// Only its owner can revoke a token; then it stops working.
	other := s.browserAs("someone-else")
	if code, _ := s.post(other, "/tokens/"+m[1]+"/revoke", nil); code != 404 {
		t.Errorf("revoke another user's token: %d", code)
	}
	s.mdGet(noRedirect, "/llms.txt", 200, auth...)
	if code, _ := s.post(c, "/tokens/"+m[1]+"/revoke", nil); code != http.StatusSeeOther {
		t.Errorf("revoke: %d", code)
	}
	s.mdGet(noRedirect, "/llms.txt", 401, auth...)
}

// browserAs is signed in as another user.
func (s *site) browserAs(subject string) *http.Client {
	s.t.Helper()
	id := randomString()
	err := s.st.CreateSession(context.Background(), hashID(id),
		store.Session{Subject: subject, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		s.t.Fatal(err)
	}
	return &http.Client{
		Transport:     cookieTransport{id},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type headerTransport struct{ header, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(h.header, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// An agent finds an event and its payload through the MCP tools: at /mcp,
// and through portal mcp's Remote catalogue, which give the same pages.
func TestMCP(t *testing.T) {
	s := catalogue(t)
	ctx := context.Background()
	tok := "pat_" + randomString()
	if _, err := s.st.CreateUserToken(ctx, hashID(tok), store.Session{Subject: "u"}, "mcp", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(s.ui.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("/mcp without a token: %d %v", resp.StatusCode, resp.Header)
	}

	hc := &http.Client{Transport: headerTransport{"Authorization", "Bearer " + tok}}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: s.ui.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	st, ct := mcp.NewInMemoryTransports()
	if _, err := agentmcp.NewServer(&agentmcp.Remote{Base: s.ui.URL, Token: tok}, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	remote, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	for _, c := range []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"search_apis", map[string]any{"query": "refund"}, []string{"com.acme.payments.refund.issued.v1"}},
		{"list_apis", nil, []string{"## team-orders", "orders-http"}},
		{"get_api", map[string]any{"api_id": "orders-http"}, []string{"# Orders API", "## Operations"}},
		{"get_operation", map[string]any{"api_id": "orders-http", "operation": "GET /orders/{orderId}"},
			[]string{"# `GET /orders/{orderId}`", "### 401, 404"}},
		{"get_event", map[string]any{"type": "com.acme.orders.order.created.v1"},
			[]string{"## Payload (data)", "- `total`", "## Consumers"}},
	} {
		got, isErr := mcpCall(t, cs, c.tool, c.args)
		if isErr {
			t.Errorf("%s: error %s", c.tool, got)
		}
		contains(t, c.tool, got, c.want...)
		if viaRemote, _ := mcpCall(t, remote, c.tool, c.args); viaRemote != got {
			t.Errorf("%s through portal mcp:\n%s\nat /mcp:\n%s", c.tool, viaRemote, got)
		}
	}
	for _, cl := range []*mcp.ClientSession{cs, remote} {
		if got, isErr := mcpCall(t, cl, "get_api", map[string]any{"api_id": "nope"}); !isErr || !strings.Contains(got, "No such API") {
			t.Errorf("unknown API: %q (error %v)", got, isErr)
		}
	}
}
