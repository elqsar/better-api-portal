package index

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/spec/eventcatalog"
	"github.com/elqsar/better-api-portal/internal/spec/openapi"
)

const example = "../../docs/spec/examples/orders-service"

func TestWords(t *testing.T) {
	for in, want := range map[string]string{
		"com.acme.orders.refund.issued.v1": "com acme orders refund issued v1",
		"/orders/{orderId}/refunds":        "orders order id refunds",
		"getHTTPServerURL":                 "get http server url",
		"order_cancel-requested":           "order cancel requested",
		"":                                 "",
	} {
		if got := Words(in); got != want {
			t.Errorf("Words(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSchemaName(t *testing.T) {
	for in, want := range map[string]string{
		"api/openapi.yaml#/components/schemas/Refund":   "Refund",
		"api/events.yaml#/messages/1/dataschema/schema": "schema",
		"api/schemas/order-created.v1.json":             "order-created.v1",
		"api/openapi.yaml#/components/schemas/a~1b":     "a/b",
	} {
		if got := SchemaName(in); got != want {
			t.Errorf("SchemaName(%q) = %q, want %q", in, got, want)
		}
	}
}

func docRefs(v *Version, kind string) []string {
	var out []string
	for _, d := range v.Docs {
		if d.Kind == kind {
			out = append(out, d.Ref)
		}
	}
	return out
}

func TestBuildEvents(t *testing.T) {
	res, _, err := eventcatalog.Parse(example, filepath.Join(example, "api/events.yaml"))
	if err != nil || res == nil {
		t.Fatal(err)
	}
	v := Build(API{ID: "orders-events", Tags: []string{"orders"}}, res.Spec)
	if len(v.Messages) != len(res.Spec.Messages) || v.Messages[0].Type != "com.acme.orders.order.created.v1" || v.Messages[0].Role != "produces" {
		t.Errorf("messages = %+v", v.Messages)
	}
	if len(v.Bindings) == 0 || v.Bindings[0].Address != "orders.events" || v.Bindings[0].Protocol != "kafka" {
		t.Errorf("bindings = %+v", v.Bindings)
	}
	if api := v.Docs[0]; api.Kind != KindAPI || api.Title != "Order events" || !strings.Contains(api.Terms, "orders events") {
		t.Errorf("api doc = %+v", api)
	}
	msg := v.Docs[1]
	if msg.Terms != "com acme orders order created v1" || !strings.Contains(msg.Body, "orders events") {
		t.Errorf("message doc = %+v", msg)
	}
	schemas := docRefs(v, KindSchema)
	if !slices.Contains(schemas, "api/schemas/order-created.v1.json") {
		t.Errorf("schema docs = %q", schemas)
	}
	for _, d := range v.Docs {
		if d.Ref == "api/schemas/order-created.v1.json" && !strings.Contains(d.Body, "customer id") {
			t.Errorf("schema body lacks its property names: %q", d.Body)
		}
	}
}

func TestBuildOpenAPI(t *testing.T) {
	res, _, err := openapi.Parse(example, filepath.Join(example, "api/openapi.yaml"))
	if err != nil || res == nil {
		t.Fatal(err)
	}
	v := Build(API{ID: "orders-http", Title: "Orders"}, res.Spec)
	if v.Docs[0].Title != "Orders" {
		t.Errorf("descriptor title should win: %+v", v.Docs[0])
	}
	ops := docRefs(v, KindOperation)
	if len(ops) != len(res.Spec.Operations) || !slices.Contains(ops, "POST /orders/{orderId}/cancellation") {
		t.Errorf("operation docs = %q", ops)
	}
	if !slices.Contains(docRefs(v, KindSchema), "api/openapi.yaml#/components/schemas/Money") {
		t.Errorf("schema docs = %q", docRefs(v, KindSchema))
	}
	for _, d := range v.Docs {
		if strings.Contains(d.Body+d.Terms, example) {
			t.Errorf("%s %s mentions the checkout path", d.Kind, d.Ref)
		}
	}
}

func TestLimit(t *testing.T) {
	long := strings.Repeat("é", maxBody)
	if got := limit(long); len(got) > maxBody || !strings.HasSuffix(got, "é") {
		t.Errorf("limit cut a rune: len %d", len(got))
	}
}
