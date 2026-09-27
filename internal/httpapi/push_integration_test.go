//go:build integration

package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/bundle"
	"github.com/elqsar/better-api-portal/internal/config"
	"github.com/elqsar/better-api-portal/internal/descriptor"
	"github.com/elqsar/better-api-portal/internal/httpapi"
	"github.com/elqsar/better-api-portal/internal/model"
	"github.com/elqsar/better-api-portal/internal/store"
	"github.com/elqsar/better-api-portal/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

const example = "../../docs/spec/examples/orders-service"

// headerAuth trusts X-Test-Repo; tests only.
type headerAuth struct{}

func (headerAuth) Authenticate(r *http.Request) (*httpapi.Identity, error) {
	repo := r.Header.Get("X-Test-Repo")
	if repo == "" {
		return nil, httpapi.ErrUnauthenticated
	}
	ref := r.Header.Get("X-Test-Ref")
	if ref == "" {
		ref = "refs/heads/main"
	}
	return &httpapi.Identity{Repo: repo, Actor: "repo:" + repo, Ref: ref, CanPush: ref == "refs/heads/main"}, nil
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newServerStore(t)
	return srv
}

// newServerStore is newServer, also returning its store.
func newServerStore(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(example, "../portal.config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.PublicURL = "https://portal.test"
	st := storetest.New(t)
	s := &httpapi.Server{Store: st, Config: cfg, Auth: headerAuth{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

// checkout copies the example and applies edits, as a commit would.
func checkout(t *testing.T, edits ...[3]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	for _, e := range edits {
		p := filepath.Join(dir, e[0])
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(b), e[1], e[2], 1)
		if s == string(b) {
			t.Fatalf("%s: %q not found", e[0], e[1])
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// body builds a push of the checkout at dir, as portal push will.
func body(t *testing.T, dir string, acks map[string]string, skip ...string) (io.Reader, string) {
	t.Helper()
	descPath := filepath.Join(dir, "portal.yaml")
	d, _, err := descriptor.Load(descPath)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	desc, _ := os.ReadFile(descPath)
	fw, _ := mw.CreateFormFile(httpapi.PartDescriptor, "portal.yaml")
	fw.Write(desc)
	for i, a := range d.APIs {
		if slices.Contains(skip, a.ID) {
			continue
		}
		c, problems, err := bundle.Load(d.Dir, d.SpecPath(i))
		if err != nil || len(problems) > 0 {
			t.Fatalf("closure: %v %+v", err, problems)
		}
		b, err := bundle.Read(c.Root, c.Files())
		if err != nil {
			t.Fatal(err)
		}
		fw, _ := mw.CreateFormFile(httpapi.PartBundlePrefix+a.ID, a.ID+".tar.zst")
		if err := b.Pack(fw); err != nil {
			t.Fatal(err)
		}
	}
	if acks != nil {
		fw, _ := mw.CreateFormField(httpapi.PartAcks)
		json.NewEncoder(fw).Encode(acks)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func post(t *testing.T, srv *httptest.Server, path, repo string, b io.Reader, ct string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, b)
	req.Header.Set("Content-Type", ct)
	if repo != "" {
		repo, ref, _ := strings.Cut(repo, "@")
		req.Header.Set("X-Test-Repo", repo)
		req.Header.Set("X-Test-Ref", ref)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func push(t *testing.T, srv *httptest.Server, path, repo, dir string, acks map[string]string) *httpapi.PushResponse {
	t.Helper()
	b, ct := body(t, dir, acks)
	code, out := post(t, srv, path, repo, b, ct)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, out)
	}
	var r httpapi.PushResponse
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// statuses is "id status" per API.
func statuses(r *httpapi.PushResponse) []string {
	var out []string
	for _, a := range r.APIs {
		out = append(out, a.ID+" "+a.Status)
	}
	return out
}

func errorRules(r *httpapi.PushResponse) []string {
	var out []string
	for _, f := range r.Findings {
		if f.Severity == model.SeverityError {
			out = append(out, f.API+" "+f.RuleID)
		}
	}
	return out
}

func expect(t *testing.T, r *httpapi.PushResponse, want ...string) {
	t.Helper()
	if got := statuses(r); !slices.Equal(got, want) {
		t.Fatalf("statuses = %q, want %q; errors: %q", got, want, errorRules(r))
	}
}

func getBundle(t *testing.T, srv *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("X-Test-Repo", "acme/anyone")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestPushLifecycle(t *testing.T) {
	srv := newServer(t)
	const repo = "acme/orders"
	head := checkout(t)

	r := push(t, srv, "/api/v1/push", repo, head, nil)
	expect(t, r, "orders-http published", "orders-events published")
	if u := r.APIs[0].URL; u != "https://portal.test/apis/orders-http/versions/2.3.0" {
		t.Errorf("url = %q", u)
	}

	// A CI retry is a no-op.
	expect(t, push(t, srv, "/api/v1/push", repo, head, nil), "orders-http unchanged", "orders-events unchanged")

	// The baseline is served back.
	resp, data := getBundle(t, srv, "/api/v1/apis/orders-http/versions/latest/bundle")
	if resp.StatusCode != 200 || resp.Header.Get(httpapi.HeaderVersion) != "2.3.0" ||
		resp.Header.Get(httpapi.HeaderLifecycle) != "production" {
		t.Fatalf("bundle: %d %v", resp.StatusCode, resp.Header)
	}
	b, err := bundle.Unpack(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := b.Hash(); h != resp.Header.Get(httpapi.HeaderContentHash) || h != r.APIs[0].ContentHash {
		t.Errorf("hash %s, header %s, pushed %s", h, resp.Header.Get(httpapi.HeaderContentHash), r.APIs[0].ContentHash)
	}

	// A breaking change on a minor bump is rejected against the stored
	// baseline, and the other API is unaffected.
	breaking := checkout(t,
		[3]string{"api/openapi.yaml", "enum: [customer_request, payment_failed, out_of_stock]", "enum: [customer_request, payment_failed]"},
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.4.0"})
	r = push(t, srv, "/api/v1/push", repo, breaking, nil)
	expect(t, r, "orders-http rejected", "orders-events unchanged")
	errs := errorRules(r)
	if len(errs) != 1 || errs[0] != "orders-http request-property-enum-value-removed" {
		t.Fatalf("errors = %q", errs)
	}
	var id string
	for _, f := range r.Findings {
		if f.Severity == model.SeverityError {
			id = f.ID
		}
	}
	if !strings.HasPrefix(id, "BRK-OA-") || r.APIs[0].BaselineVersion != "2.3.0" {
		t.Fatalf("id = %q, api = %+v", id, r.APIs[0])
	}
	if f := r.Findings[0]; strings.Contains(f.File, os.TempDir()) {
		t.Errorf("temporary path in findings: %+v", f)
	}

	// Acknowledged, it publishes; the old version stays the baseline until then.
	expect(t, push(t, srv, "/api/v1/push", repo, breaking, map[string]string{id: "nobody sends out_of_stock"}),
		"orders-http published", "orders-events unchanged")
	if resp, _ := getBundle(t, srv, "/api/v1/apis/orders-http/versions/latest/bundle"); resp.Header.Get(httpapi.HeaderVersion) != "2.4.0" {
		t.Errorf("latest = %s", resp.Header.Get(httpapi.HeaderVersion))
	}
	if resp, _ := getBundle(t, srv, "/api/v1/apis/orders-http/versions/2.3.0/bundle"); resp.StatusCode != 200 {
		t.Errorf("2.3.0: %d", resp.StatusCode)
	}

	// Published versions are immutable.
	changed := checkout(t,
		[3]string{"api/openapi.yaml", "summary: Get an order", "summary: Fetch an order"},
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.4.0"})
	r = push(t, srv, "/api/v1/push", repo, changed, nil)
	expect(t, r, "orders-http rejected", "orders-events unchanged")
	if errs := errorRules(r); !slices.Contains(errs, "orders-http version-immutable") {
		t.Errorf("errors = %q", errs)
	}
}

func TestPushClaims(t *testing.T) {
	srv := newServer(t)
	head := checkout(t)
	expect(t, push(t, srv, "/api/v1/push", "acme/orders", head, nil), "orders-http published", "orders-events published")

	r := push(t, srv, "/api/v1/push", "acme/fork", checkout(t,
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.3.1"}), nil)
	expect(t, r, "orders-http rejected", "orders-events rejected")
	if errs := errorRules(r); !slices.Equal(errs, []string{"orders-http api-claimed", "orders-events api-claimed"}) {
		t.Errorf("errors = %q", errs)
	}
	// The claimed version wasn't touched.
	if resp, _ := getBundle(t, srv, "/api/v1/apis/orders-http/versions/latest/bundle"); resp.Header.Get(httpapi.HeaderVersion) != "2.3.0" {
		t.Errorf("latest = %s", resp.Header.Get(httpapi.HeaderVersion))
	}
}

func TestPushRejectedFirstPushClaimsNothing(t *testing.T) {
	srv := newServer(t)
	// An owner that isn't a configured team fails every API.
	bad := checkout(t, [3]string{"portal.yaml", "owner: team-orders", "owner: team-unknown"})
	r := push(t, srv, "/api/v1/push", "acme/orders", bad, nil)
	expect(t, r, "orders-http rejected", "orders-events rejected")
	if resp, _ := getBundle(t, srv, "/api/v1/apis/orders-http/versions/latest/bundle"); resp.StatusCode != 404 {
		t.Errorf("rejected push stored something: %d", resp.StatusCode)
	}
	// So another repo can still claim the id.
	expect(t, push(t, srv, "/api/v1/push", "acme/other", checkout(t), nil), "orders-http published", "orders-events published")
}

func TestCheckStoresNothing(t *testing.T) {
	srv := newServer(t)
	// A pull request's ref can't push, but it can check.
	r := push(t, srv, "/api/v1/check", "acme/orders@refs/pull/7/merge", checkout(t), nil)
	expect(t, r, "orders-http accepted", "orders-events accepted")
	if resp, _ := getBundle(t, srv, "/api/v1/apis/orders-http/versions/latest/bundle"); resp.StatusCode != 404 {
		t.Errorf("check stored something: %d", resp.StatusCode)
	}
}

func TestPushConsumesUnknown(t *testing.T) {
	srv := newServer(t)
	r := push(t, srv, "/api/v1/push", "acme/orders", checkout(t), nil)
	var warned []string
	for _, f := range r.Findings {
		if f.RuleID == "consumes-unknown-api" {
			warned = append(warned, f.Pointer)
		}
	}
	// payments-events and customers-http aren't in the portal: warnings only.
	if !slices.Equal(warned, []string{"/consumes/0/api", "/consumes/1/api"}) {
		t.Errorf("warnings at %q", warned)
	}
	expect(t, r, "orders-http published", "orders-events published")
}

func TestPushBadRequests(t *testing.T) {
	srv := newServer(t)
	head := checkout(t)
	for name, tc := range map[string]struct {
		repo string
		body func() (io.Reader, string)
		code int
	}{
		"unauthenticated": {"", func() (io.Reader, string) { return body(t, head, nil) }, 401},
		"pull request":    {"acme/orders@refs/pull/7/merge", func() (io.Reader, string) { return body(t, head, nil) }, 403},
		"missing bundle":  {"acme/orders", func() (io.Reader, string) { return body(t, head, nil, "orders-events") }, 400},
		"not multipart":   {"acme/orders", func() (io.Reader, string) { return strings.NewReader("{}"), "application/json" }, 400},
		"empty ack reason": {"acme/orders", func() (io.Reader, string) {
			return body(t, head, map[string]string{"BRK-OA-000000": " "})
		}, 400},
		"corrupt bundle": {"acme/orders", func() (io.Reader, string) {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			fw, _ := mw.CreateFormFile(httpapi.PartDescriptor, "portal.yaml")
			fw.Write([]byte("apiVersion: portal/v1\n"))
			fw, _ = mw.CreateFormFile(httpapi.PartBundlePrefix+"orders-http", "x")
			fw.Write([]byte("not zstd"))
			mw.Close()
			return &buf, mw.FormDataContentType()
		}, 400},
	} {
		t.Run(name, func(t *testing.T) {
			b, ct := tc.body()
			code, out := post(t, srv, "/api/v1/push", tc.repo, b, ct)
			if code != tc.code {
				t.Errorf("status %d, want %d: %s", code, tc.code, out)
			}
			var e httpapi.ErrorResponse
			if err := json.Unmarshal(out, &e); err != nil || e.Error == "" {
				t.Errorf("body %s: %v", out, errors.Join(err, fmt.Errorf("no error message")))
			}
		})
	}
}

// service writes a checkout from files (path → content).
func service(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, content := range files {
		path := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// rival is team-payments' service: one events API per id, each declaring
// the types, produced to Kafka orders.events, the topic orders-events uses.
func rival(t *testing.T, version string, ids []string, types ...string) string {
	t.Helper()
	desc := "apiVersion: portal/v1\nowner: team-payments\napis:\n"
	for _, id := range ids {
		desc += fmt.Sprintf("  - { id: %s, kind: cloudevents, spec: api/events.yaml, lifecycle: production }\n", id)
	}
	events := fmt.Sprintf(`eventcatalog: "1.0"
title: Rival events
version: %s
defaults:
  source: /payments-service
  datacontenttype: application/json
  bindings:
    - kafka: { topic: orders.events, key: { from: data, pointer: /orderId }, mode: binary }
messages:
`, version)
	for _, ty := range types {
		events += fmt.Sprintf(`  - type: %s
    role: produces
    summary: Something happened.
    description: Emitted when it happens.
    dataschema:
      schema: { type: object, required: [orderId], properties: { orderId: { type: string } } }
`, ty)
	}
	return service(t, map[string]string{"portal.yaml": desc, "api/events.yaml": events})
}

func findings(r *httpapi.PushResponse, rule string) []model.Finding {
	var out []model.Finding
	for _, f := range r.Findings {
		if f.RuleID == rule {
			out = append(out, f)
		}
	}
	return out
}

func score(r *httpapi.PushResponse, id string) int {
	for _, a := range r.APIs {
		if a.ID == id {
			return a.Score
		}
	}
	return -1
}

func TestPushPortalRules(t *testing.T) {
	srv := newServer(t)
	expect(t, push(t, srv, "/api/v1/push", "acme/orders", checkout(t), nil), "orders-http published", "orders-events published")

	// Another team declares an orders type: rejected, however it is
	// declared. The shared topic is a warning.
	r := push(t, srv, "/api/v1/push", "acme/payments",
		rival(t, "1.0.0", []string{"payments-events"}, "com.acme.payments.payment.captured.v1", "com.acme.orders.order.created.v1"), nil)
	expect(t, r, "payments-events rejected")
	dup := findings(r, "ce-type-unique")
	if len(dup) != 1 || dup[0].API != "payments-events" || dup[0].File != "api/events.yaml" || dup[0].Pointer != "/messages/1" || dup[0].Line == 0 ||
		!strings.Contains(dup[0].Message, "com.acme.orders.order.created.v1 is already declared by orders-events (produces, 1.4.0)") ||
		!strings.Contains(dup[0].Message, "list it under consumes in portal.yaml") {
		t.Errorf("ce-type-unique = %+v", dup)
	}
	topic := findings(r, "ce-topic-single-owner")
	if len(topic) != 1 || topic[0].Severity != model.SeverityWarn || topic[0].Pointer != "/messages/0" ||
		!strings.Contains(topic[0].Message, "kafka orders.events is also produced to by orders-events (team-orders)") {
		t.Errorf("ce-topic-single-owner = %+v", topic)
	}
	rejectedScore := score(r, "payments-events")

	// The dry run gives the same verdict.
	expect(t, push(t, srv, "/api/v1/check", "acme/payments",
		rival(t, "1.0.0", []string{"payments-events"}, "com.acme.payments.payment.captured.v1", "com.acme.orders.order.created.v1"), nil),
		"payments-events rejected")

	// Its own types publish; the shared topic still warns, and costs 2
	// points where the duplicate type cost 10.
	r = push(t, srv, "/api/v1/push", "acme/payments",
		rival(t, "1.0.0", []string{"payments-events"}, "com.acme.payments.payment.captured.v1", "com.acme.payments.refund.issued.v1"), nil)
	expect(t, r, "payments-events published")
	if len(findings(r, "ce-type-unique")) != 0 || len(findings(r, "ce-topic-single-owner")) != 1 {
		t.Errorf("findings %+v", r.Findings)
	}
	if got := score(r, "payments-events"); got != rejectedScore+10 {
		t.Errorf("score %d, want %d (the rejected push's %d without the error)", got, rejectedScore+10, rejectedScore)
	}

	// The owner's next version sees the other team on its topic, and isn't
	// flagged for its own types.
	r = push(t, srv, "/api/v1/push", "acme/orders", checkout(t,
		[3]string{"api/events.yaml", "version: 1.4.0", "version: 1.4.1"}), nil)
	expect(t, r, "orders-http unchanged", "orders-events published")
	if len(findings(r, "ce-type-unique")) != 0 {
		t.Errorf("own types flagged: %+v", findings(r, "ce-type-unique"))
	}
	if topic := findings(r, "ce-topic-single-owner"); len(topic) != 1 || !strings.Contains(topic[0].Message, "payments-events (team-payments)") {
		t.Errorf("owner's topic warning = %+v", topic)
	}
}

// Two APIs of one push can't both declare a type; one team's APIs may
// share a topic.
func TestPushPortalRulesWithinPush(t *testing.T) {
	srv := newServer(t)
	r := push(t, srv, "/api/v1/push", "acme/payments",
		rival(t, "1.0.0", []string{"payments-events", "refunds-events"}, "com.acme.payments.refund.issued.v1"), nil)
	expect(t, r, "payments-events rejected", "refunds-events rejected")
	var got []string
	for _, f := range findings(r, "ce-type-unique") {
		got = append(got, f.API+": "+f.Message[:strings.Index(f.Message, ";")])
	}
	want := []string{
		"payments-events: com.acme.payments.refund.issued.v1 is already declared by refunds-events (in this push)",
		"refunds-events: com.acme.payments.refund.issued.v1 is already declared by payments-events (in this push)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ce-type-unique = %q, want %q", got, want)
	}
	if topic := findings(r, "ce-topic-single-owner"); len(topic) != 0 {
		t.Errorf("same-team topic warned: %+v", topic)
	}
}

func TestPushUnchangedUpdatesMetadata(t *testing.T) {
	srv, st := newServerStore(t)
	const repo = "acme/orders"
	expect(t, push(t, srv, "/api/v1/push", repo, checkout(t), nil), "orders-http published", "orders-events published")

	consumes := func() []string {
		t.Helper()
		deps, _, err := st.Dependencies(t.Context(), "orders-http")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range deps {
			out = append(out, d.To)
		}
		return out
	}
	updated := func(r *httpapi.PushResponse) []bool {
		var out []bool
		for _, a := range r.APIs {
			out = append(out, a.MetadataUpdated)
		}
		return out
	}
	if got := consumes(); !slices.Equal(got, []string{"customers-http", "payments-events"}) {
		t.Fatalf("consumes = %q", got)
	}

	// Only the descriptor changes: a new dependency, a new tag.
	edited := checkout(t,
		[3]string{"portal.yaml", "  - api: customers-http", "  - api: customers-http\n  - api: inventory-http"},
		[3]string{"portal.yaml", "tags: [orders, checkout]", "tags: [orders, checkout, returns]"})
	r := push(t, srv, "/api/v1/check", repo, edited, nil)
	expect(t, r, "orders-http unchanged", "orders-events unchanged")
	if got := updated(r); !slices.Equal(got, []bool{true, true}) {
		t.Errorf("dry run: metadataUpdated = %v", got)
	}
	if got := consumes(); len(got) != 2 {
		t.Errorf("the dry run stored consumes %q", got)
	}

	r = push(t, srv, "/api/v1/push", repo, edited, nil)
	expect(t, r, "orders-http unchanged", "orders-events unchanged")
	if got := updated(r); !slices.Equal(got, []bool{true, true}) {
		t.Errorf("metadataUpdated = %v", got)
	}
	if got := consumes(); !slices.Equal(got, []string{"customers-http", "inventory-http", "payments-events"}) {
		t.Errorf("consumes = %q", got)
	}

	// A retry changes nothing.
	r = push(t, srv, "/api/v1/push", repo, edited, nil)
	if got := updated(r); !slices.Equal(got, []bool{false, false}) {
		t.Errorf("retry: metadataUpdated = %v", got)
	}

	// A newer version, then the old one again with the old descriptor (an
	// old tag's rebuild): orders-http keeps the newer push's metadata,
	// while orders-events, whose latest version it still is, takes it.
	newer := checkout(t,
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.4.0"},
		[3]string{"portal.yaml", "  - api: customers-http", "  - api: customers-http\n  - api: inventory-http"},
		[3]string{"portal.yaml", "tags: [orders, checkout]", "tags: [orders, checkout, returns]"})
	expect(t, push(t, srv, "/api/v1/push", repo, newer, nil), "orders-http published", "orders-events unchanged")
	r = push(t, srv, "/api/v1/push", repo, checkout(t), nil)
	if got := updated(r); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("old version: metadataUpdated = %v", got)
	}
	if got := consumes(); !slices.Equal(got, []string{"customers-http", "inventory-http", "payments-events"}) {
		t.Errorf("an old version's push changed consumes to %q", got)
	}
}
