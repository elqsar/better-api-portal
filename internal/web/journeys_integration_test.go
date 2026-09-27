//go:build integration

package web

// Acceptance tests for the read UI's journeys (docs/spec/01-product.md,
// J3–J5): each step's page is checked for what the journey needs, and its
// <main> is compared with a golden file, so any change to these pages
// shows up for review. To rewrite them: task test:journeys -- -update
//
// J3's latency target is TestSearchLatency500 in internal/store.

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

const payments = "testdata/payments-service"

var (
	mainRE = regexp.MustCompile(`(?s)<main[^>]*>(.*)</main>`)
	// Push times differ on every run.
	timeRE     = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC`)
	datetimeRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`)
)

// golden compares a page's <main>, without blank lines or trailing spaces
// and with times replaced, to testdata/journeys/<name>.
func golden(t *testing.T, name, body string) {
	t.Helper()
	m := mainRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s: no <main> in\n%s", name, body)
	}
	var lines []string
	for _, l := range strings.Split(m[1], "\n") {
		if l = strings.TrimRight(l, " \t"); strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	got = timeRE.ReplaceAllString(got, "<time>")
	got = datetimeRE.ReplaceAllString(got, "<time>")

	path := filepath.Join("testdata", "journeys", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file; rerun with -update and review the diff\n%s", name, got)
	}
}

// page gets a page that must load.
func (s *site) page(path string) string {
	s.t.Helper()
	code, body, _ := s.get(s.browser(), path)
	if code != 200 {
		s.t.Fatalf("%s: status %d\n%s", path, code, body)
	}
	return body
}

// catalogue publishes the example and the payments service.
func catalogue(t *testing.T) *site {
	s := newSite(t)
	for _, p := range []struct {
		repo, dir string
	}{{"acme/orders", example}, {"acme/payments", payments}} {
		for id, st := range s.pushDir(p.repo, p.dir) {
			if st != "published" {
				t.Fatalf("%s: %s", id, st)
			}
		}
	}
	return s
}

// J3: a consumer searches "refund", then opens the API page.
func TestJourneyFindAnAPI(t *testing.T) {
	s := catalogue(t)

	// 1–2. Operations, event types and schemas mentioning the word, grouped
	// by API with owner and lifecycle.
	body := s.page("/search?q=refund")
	contains(t, "J3 search", body,
		`<a href="/apis/payments-http/versions/1.2.0">payments-http</a>`,
		`<a href="/apis/payments-events/versions/1.0.0">payments-events</a>`,
		`<code>POST /orders/{orderId}/refunds</code>`,
		`<code>com.acme.payments.refund.issued.v1</code>`,
		`<span class="badge">schema</span> <a href="/apis/payments-http/versions/1.2.0/docs"><code>api/openapi.yaml#/components/schemas/Refund</code>`,
		`<span class="muted owner">Payments</span>`, `<span class="badge lc-production">production</span>`)
	golden(t, "j3-search.html", body)

	// 3. The API page: docs, versions, owner contact, lint score,
	// environments.
	body = s.page("/apis/payments-http/versions/1.2.0")
	contains(t, "J3 API page", body,
		`href="/apis/payments-http/versions/1.2.0/docs"`, `href="/apis/payments-http/versions"`,
		`https://chat.internal/channels/team-payments`, "https://payments.prod.internal")
	golden(t, "j3-api.html", body)
	s.page("/apis/payments-http/versions/1.2.0/docs")
	if doc := s.page("/apis/payments-http/versions/1.2.0/openapi.json"); !strings.Contains(doc, `"operationId":"createRefund"`) {
		t.Errorf("the docs' document lacks the operation:\n%s", doc)
	}
}

// J4: a consumer opens an event type.
func TestJourneyUnderstandAnEvent(t *testing.T) {
	s := catalogue(t)

	body := s.page("/events/com.acme.orders.order.created.v1")
	contains(t, "J4 event",
		body,
		// 2. Description, CloudEvents attributes, payload tree and example,
		// bindings.
		"Emitted once, after the order is persisted and payment is authorised.",
		"/orders-service/{region}", "application/json",
		"orderId", "ord_123",
		"orders.events", "binary",
		// 3. Who produces it, and who consumes it.
		`href="/apis/orders-events/versions/1.4.0"`, `href="/apis/payments-events`)
	golden(t, "j4-event.html", body)
}

// J5: from an API's version list, pick two versions and compare them.
func TestJourneyCompareVersions(t *testing.T) {
	s := catalogue(t)
	// A major release: one type removed (breaking), a payload field added,
	// a summary reworded.
	st := s.push(
		[3]string{"api/events.yaml", "version: 1.4.0", "version: 2.0.0"},
		[3]string{"api/events.yaml", "summary: An order was placed.", "summary: An order was placed and paid."},
		[3]string{"api/events.yaml", `  - type: com.acme.orders.order.cancel.requested.v1
    role: receives`, `  - type: com.acme.orders.order.cancel.requested.v2
    role: receives`},
		[3]string{"api/schemas/order-created.v1.json", `"customerId": { "type": "string" },`,
			`"customerId": { "type": "string" },
    "channel": { "type": "string", "description": "Where the order was placed." },`})
	if st["orders-events"] != "published" {
		t.Fatalf("statuses %v", st)
	}

	// 1. The version list offers the two versions.
	body := s.page("/apis/orders-events/versions")
	contains(t, "J5 versions", body, `action="/apis/orders-events/diff"`,
		`<option selected>1.4.0</option>`, `<option selected>2.0.0</option>`)
	golden(t, "j5-versions.html", body)

	// 2. The structured diff, breaking items flagged, and the raw diff.
	body = s.page("/apis/orders-events/diff?from=1.4.0&to=2.0.0")
	contains(t, "J5 changes", body, `<span class="badge impact-breaking">breaking</span>`,
		"com.acme.orders.order.cancel.requested.v1", `<span class="badge impact-additive">additive</span>`,
		`<span class="badge impact-docs">docs</span>`)
	// A removed type has no event page to link to.
	if code, _, _ := s.get(s.browser(), "/events/com.acme.orders.order.cancel.requested.v1"); code != 404 {
		t.Errorf("removed type's page: %d, want 404", code)
	}
	if strings.Contains(body, `href="/events/com.acme.orders.order.cancel.requested.v1"`) {
		t.Error("the removed type links to its (missing) event page")
	}
	golden(t, "j5-changes.html", body)

	body = s.page("/apis/orders-events/diff?from=1.4.0&to=2.0.0&view=raw")
	contains(t, "J5 raw", body, "api/events.yaml", "api/schemas/order-created.v1.json",
		`<td class="code">    summary: An order was placed and paid.</td>`)
	golden(t, "j5-raw.html", body)
}
