//go:build integration

package web

import (
	"net/http"
	"strings"
	"testing"
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
	contains(t, "llms.txt", body, "# API portal",
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
	contains(t, "llms-full", full, "# API portal: team-payments", "\n## Payment events\n",
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
