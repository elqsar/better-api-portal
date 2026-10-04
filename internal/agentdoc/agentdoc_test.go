package agentdoc

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/bundle"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("%s differs from the golden file; rerun with -update and review the diff\n%s", name, got)
	}
}

// load bundles and parses a fixture spec, as the portal does on push.
func load(t *testing.T, root, entry string) *Version {
	t.Helper()
	c, problems, err := bundle.Load(root, filepath.Join(root, entry))
	if err != nil || len(problems) > 0 {
		t.Fatalf("bundle %s: %v %v", entry, err, problems)
	}
	b, err := bundle.Read(root, c.Files())
	if err != nil {
		t.Fatal(err)
	}
	v, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

var urls = URLs{Base: "https://portal.internal"}

const (
	orders   = "../../docs/spec/examples/orders-service"
	payments = "../web/testdata/payments-service"
)

func TestOpenAPI(t *testing.T) {
	v := load(t, orders, "api/openapi.yaml")
	a := &API{ID: "orders-http", Kind: "openapi", Title: "Orders API", Owner: "team-orders", Lifecycle: "production",
		System: "commerce", Repo: "github.com/acme/orders-service", Version: v.Spec.Version, Tags: []string{"orders"},
		Links:        []Link{{"#team-orders", "https://chat.internal/channels/team-orders"}},
		Environments: []Environment{{Name: "prod", URL: "https://orders.prod.internal"}},
		Consumers:    []Dependency{{Repo: "github.com/acme/web-shop", Owner: "team-web", APIs: []string{"web-shop"}, To: "orders-http"}},
	}
	golden(t, "orders-http.md", APIPage(a, v, urls, false, 0))
	for _, op := range v.Spec.Operations {
		golden(t, "orders-http."+OperationKey(op.Method, op.Path, op.OperationID)+".md", OperationPage(a, v, op, urls, 0))
	}
}

func TestEvents(t *testing.T) {
	v := load(t, payments, "api/events.yaml")
	a := &API{ID: "payments-events", Kind: "cloudevents", Owner: "team-payments", Lifecycle: "deprecated",
		Sunset: "2027-01-31", Version: v.Spec.Version,
		Environments: []Environment{{Name: "prod", Broker: "kafka-prod"}},
		Consumes:     []Dependency{{To: "orders-events", Types: []string{"com.acme.orders.order.created.v1"}}},
	}
	golden(t, "payments-events.full.md", APIPage(a, v, urls, true, 0))
	consumers := []Dependency{{Repo: "github.com/acme/ledger", Owner: "team-finance", APIs: []string{"ledger-events"}}}
	golden(t, "payments-events.refund.md", MessagePage(a, v, v.Spec.Messages[1], consumers, urls, 0))
}

func TestCatalogue(t *testing.T) {
	golden(t, "llms.txt", Catalogue("Acme API portal", []Entry{
		{ID: "payments-http", Kind: "openapi", Title: "Payments API", Owner: "team-payments", Lifecycle: "production", Version: "1.2.0",
			Description: "Payments and refunds.\nIdempotent writes"},
		{ID: "orders-http", Kind: "openapi", Title: "Orders API", Owner: "team-orders", Lifecycle: "production", Version: "2.3.0"},
		{ID: "orders-events", Kind: "cloudevents", Owner: "team-orders", Lifecycle: "deprecated", Version: "1.4.0"},
		{ID: "legacy", Kind: "openapi", Owner: "team-orders", Lifecycle: "retired", Version: "0.9.0"},
	}, urls))
}

func TestOperationKey(t *testing.T) {
	for _, c := range []struct{ method, path, id, want string }{
		{"GET", "/orders/{orderId}", "getOrder", "getOrder"},
		{"GET", "/orders/{orderId}", "", "get-orders-orderId"},
		{"POST", "/", "", "post-"},
	} {
		if got := OperationKey(c.method, c.path, c.id); got != c.want {
			t.Errorf("OperationKey(%s %s %q) = %q, want %q", c.method, c.path, c.id, got, c.want)
		}
	}
}

func TestSchemaBudget(t *testing.T) {
	// A wide schema stops at maxSchemaLines with a count of the rest.
	var b strings.Builder
	b.WriteString(`{"type": "object", "properties": {`)
	for i := range maxSchemaLines + 5 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"f` + strconv.Itoa(i) + `": {"type": "string"}`)
	}
	b.WriteString(`}}`)
	v := &Version{Docs: map[string]any{}}
	var doc any
	if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatal(err)
	}
	v.Docs["s.json"] = doc
	w := &writer{}
	w.writeSchema(v, "s.json")
	got := w.String()
	if n := strings.Count("\n"+got, "\n- `"); n != maxSchemaLines || !strings.Contains(got, "… 5 more lines not shown") {
		t.Errorf("%d fields shown:\n%s", n, got)
	}
}
