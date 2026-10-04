package agentmcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fake records the calls and answers with their arguments.
type fake struct{ calls []string }

func (f *fake) record(s string) (string, error) {
	f.calls = append(f.calls, s)
	if strings.Contains(s, "missing") {
		return "", errors.New("No such API. Search for it.")
	}
	return "page " + s, nil
}

func (f *fake) Index(context.Context) (string, error) { return f.record("index") }
func (f *fake) Search(_ context.Context, q Query) (string, error) {
	return f.record("search " + q.Q + " kind=" + q.Kind + " team=" + q.Team + " api=" + q.API)
}
func (f *fake) API(_ context.Context, id, version string) (string, error) {
	return f.record("api " + id + " " + version)
}
func (f *fake) Operation(_ context.Context, id, version, key string) (string, error) {
	return f.record("operation " + id + " " + version + " " + key)
}
func (f *fake) Event(_ context.Context, typ string) (string, error) { return f.record("event " + typ) }

func connect(t *testing.T, c Catalogue) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := NewServer(c, "test").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
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

func TestTools(t *testing.T) {
	f := &fake{}
	cs := connect(t, f)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s isn't marked read-only", tl.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"get_api", "get_event", "get_operation", "list_apis", "search_apis"}; !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}

	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_apis", nil, "page index"},
		{"search_apis", map[string]any{"query": "refund", "kind": "operation"}, "page search refund kind=operation team= api="},
		{"get_api", map[string]any{"api_id": "orders-http"}, "page api orders-http "},
		{"get_api", map[string]any{"api_id": "orders-http", "version": "2.3.0"}, "page api orders-http 2.3.0"},
		{"get_operation", map[string]any{"api_id": "orders-http", "operation": "getOrder"}, "page operation orders-http  getOrder"},
		// An operation by method and path becomes the pages' key.
		{"get_operation", map[string]any{"api_id": "orders-http", "operation": "GET /orders/{orderId}"},
			"page operation orders-http  get-orders-orderId"},
		{"get_event", map[string]any{"type": "com.acme.orders.order.created.v1"}, "page event com.acme.orders.order.created.v1"},
	} {
		if got, isErr := call(t, cs, c.tool, c.args); got != c.want || isErr {
			t.Errorf("%s %v = %q (error %v), want %q", c.tool, c.args, got, isErr, c.want)
		}
	}

	// A catalogue error is a tool error the agent can read.
	if got, isErr := call(t, cs, "get_api", map[string]any{"api_id": "missing"}); !isErr || !strings.Contains(got, "No such API") {
		t.Errorf("missing API: %q (error %v)", got, isErr)
	}
	// Missing required arguments are refused before the catalogue.
	n := len(f.calls)
	if _, isErr := call(t, cs, "get_event", map[string]any{}); !isErr || len(f.calls) != n {
		t.Errorf("get_event without a type: error %v, calls %v", isErr, f.calls[n:])
	}
}

func TestRemote(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat_x" {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("# Token not accepted\n"))
			return
		}
		got = append(got, r.URL.RequestURI())
		if strings.Contains(r.URL.Path, "nope") {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("# No such API\n\nNo API has the id nope.\n"))
			return
		}
		if strings.Contains(r.URL.Path, "html") {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html>proxy error</html>"))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	ctx := context.Background()
	r := &Remote{Base: srv.URL, Token: "pat_x"}

	r.Index(ctx)
	r.Search(ctx, Query{Q: "refund issued", Team: "team-payments"})
	r.API(ctx, "orders-http", "")
	r.API(ctx, "orders-http", "2.3.0")
	r.Operation(ctx, "orders-http", "", "getOrder")
	r.Event(ctx, "com.acme.orders.order.created.v1")
	want := []string{
		"/llms.txt",
		"/search.md?q=refund+issued&team=team-payments",
		"/apis/orders-http.md",
		"/apis/orders-http/versions/2.3.0.md",
		"/apis/orders-http/versions/latest/operations/getOrder.md",
		"/events/com.acme.orders.order.created.v1.md",
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests\n%v\nwant\n%v", got, want)
	}

	// The portal's explanation is the error; other bodies aren't passed on.
	if _, err := r.API(ctx, "nope", ""); err == nil || err.Error() != "# No such API\n\nNo API has the id nope." {
		t.Errorf("404: %v", err)
	}
	if _, err := r.API(ctx, "html", ""); err == nil || err.Error() != "the portal answered 502 Bad Gateway" {
		t.Errorf("502: %v", err)
	}
	r.Token = "pat_wrong"
	if _, err := r.Index(ctx); err == nil || !strings.Contains(err.Error(), "$PORTAL_TOKEN") {
		t.Errorf("401: %v", err)
	}
}
