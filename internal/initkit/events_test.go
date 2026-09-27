package initkit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/spec/eventcatalog"
)

func TestEvents(t *testing.T) {
	out := "testdata/events.yaml"
	b, types, err := Events("testdata/schemas", out, EventsOptions{Prefix: "com.acme.payments", Title: "Payment events"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range types {
		s := e.Type + " " + e.Schema
		if e.FromID {
			s += " $id"
		}
		if len(e.Problems) > 0 {
			s += " ✗ " + strings.Join(e.Problems, "; ")
		}
		got = append(got, s)
	}
	// common/money.json is referenced, not a message; the OpenAPI file isn't a
	// schema. Majors come from the file name or a URL $id.
	want := []string{
		"com.acme.payments.payment.captured.v1 ./schemas/PaymentCaptured.json",
		"com.acme.payments.dispute.opened.v3 ./schemas/dispute_opened.v3.yaml",
		"com.acme.payments.payment.failed.v1 ./schemas/kept.json $id",
		"Acme.Payments.Legacy ./schemas/legacy.json $id ✗ not reverse-DNS, lower-case and ending in .vN (ce-type-format); doesn't start with com.acme.payments. (ce-type-prefix)",
		"com.acme.payments.refund.v2 ./schemas/refund.json",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("types:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, s := range []string{"title: Payment events\n", "summary: A payment was captured.\n", "# bindings:"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("draft lacks %q:\n%s", s, b)
		}
	}
}

// TestEventsExample drafts the example's catalogue from its schemas: the
// types that have schema files come out as the example names them, and the
// draft parses.
func TestEventsExample(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join(example, "orders-service"))); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "api/events.yaml")
	b, types, err := Events(filepath.Join(root, "api/schemas"), out, EventsOptions{Prefix: "com.acme.orders.", KafkaTopic: "orders.events"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range types {
		got = append(got, e.Type)
	}
	if want := []string{"com.acme.orders.order.cancel.requested.v1", "com.acme.orders.order.created.v1"}; !slices.Equal(got, want) {
		t.Errorf("types = %q, want %q", got, want)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	res, findings, err := eventcatalog.Parse(root, out)
	if err != nil || res == nil || len(findings) > 0 {
		t.Errorf("draft: %v %+v\n%s", err, findings, b)
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string]string{"orderCreated": "order.created", "order-created": "order.created",
		"HTTPRequestSent": "http.request.sent", "dispute_opened": "dispute.opened", "Refund2Issued": "refund2.issued"} {
		if got := strings.Join(words(in), "."); got != want {
			t.Errorf("words(%q) = %q, want %q", in, got, want)
		}
	}
}
