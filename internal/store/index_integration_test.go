//go:build integration

package store_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"better-api-portal/internal/index"
	"better-api-portal/internal/spec/eventcatalog"
	"better-api-portal/internal/spec/openapi"
	"better-api-portal/internal/store"
)

const example = "../../docs/spec/examples/orders-service"

func eventsIndex(t *testing.T) *index.Version {
	t.Helper()
	res, _, err := eventcatalog.Parse(example, filepath.Join(example, "api/events.yaml"))
	if err != nil || res == nil {
		t.Fatal(err)
	}
	return index.Build(index.API{ID: "orders-events"}, res.Spec)
}

func httpIndex(t *testing.T) *index.Version {
	t.Helper()
	res, _, err := openapi.Parse(example, filepath.Join(example, "api/openapi.yaml"))
	if err != nil || res == nil {
		t.Fatal(err)
	}
	return index.Build(index.API{ID: "orders-http", Title: "Orders"}, res.Spec)
}

func eventsPush(repo int64, semver, hash, status string, idx *index.Version) store.Push {
	p := push(repo, semver, hash, status)
	p.Version.Prerelease = strings.Contains(semver, "-")
	p.API = store.API{ID: "orders-events", Kind: "cloudevents", Owner: "team-orders", Lifecycle: "production",
		Meta: map[string]any{"consumes": []map[string]any{{"api": "payments-events", "types": []string{"com.acme.payments.captured.v1"}}}}}
	p.Index = idx
	return p
}

func TestIndex(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	repo, _ := s.Repo(ctx, "acme/orders")
	idx := eventsIndex(t)

	// A pre-release is latest only while there is no release.
	rc, err := s.Record(ctx, eventsPush(repo, "2.0.0-rc.1", "sha256:rc", store.StatusPublished, idx))
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := s.API(ctx, "orders-events"); a.LatestVersionID != rc {
		t.Errorf("latest = %d, want the pre-release %d", a.LatestVersionID, rc)
	}
	v1, err := s.Record(ctx, eventsPush(repo, "1.4.0", "sha256:a", store.StatusPublished, idx))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Record(ctx, eventsPush(repo, "1.5.0", "sha256:b", store.StatusRejected, idx)); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.API(ctx, "orders-events"); a.LatestVersionID != v1 {
		t.Errorf("latest = %d, want 1.4.0 (%d): releases beat pre-releases, rejected don't count", a.LatestVersionID, v1)
	}

	m, err := s.Model(ctx, v1)
	if err != nil || m == nil || m.Version != "1.4.0" || len(m.Messages) != len(idx.Messages) {
		t.Fatalf("model = %+v, %v", m, err)
	}
	roles, err := s.MessageRoles(ctx, "com.acme.orders.order.created.v1")
	if err != nil || len(roles) != 1 || roles[0] != (store.MessageRole{APIID: "orders-events", Role: "produces", Semver: "1.4.0"}) {
		t.Errorf("roles = %+v, %v", roles, err)
	}
	consumes, consumers, err := s.Dependencies(ctx, "orders-events")
	if err != nil || len(consumes) != 1 || consumes[0].To != "payments-events" ||
		!slices.Equal(consumes[0].Types, []string{"com.acme.payments.captured.v1"}) || len(consumers) != 0 {
		t.Errorf("dependencies = %+v %+v, %v", consumes, consumers, err)
	}
	if _, consumers, _ := s.Dependencies(ctx, "payments-events"); len(consumers) != 1 {
		t.Errorf("payments-events consumers = %+v", consumers)
	}

	// The HTTP API is searchable too; identifiers are split, words stemmed.
	if _, err := s.Record(ctx, func() store.Push {
		p := push(repo, "2.3.0", "sha256:h", store.StatusPublished)
		p.Index = httpIndex(t)
		return p
	}()); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, "cancel", 20)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, h := range hits {
		refs = append(refs, h.APIID+" "+h.Kind+" "+h.Ref)
	}
	for _, want := range []string{
		"orders-events message com.acme.orders.order.cancelled.v1",
		"orders-http operation POST /orders/{orderId}/cancellation",
	} {
		if !slices.Contains(refs, want) {
			t.Errorf("search cancel: no %q in %q", want, refs)
		}
	}
	for _, h := range hits {
		if h.APIID == "orders-events" && h.Semver != "1.4.0" {
			t.Errorf("hit from a version that isn't latest: %+v", h)
		}
	}

	// Reindexing replaces rather than duplicates.
	if err := s.Reindex(ctx, v1, idx); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Search(ctx, "cancel", 20); len(again) != len(hits) {
		t.Errorf("after reindex: %d hits, want %d", len(again), len(hits))
	}
	vs, err := s.PublishedVersions(ctx)
	if err != nil || len(vs) != 3 {
		t.Errorf("published versions = %+v, %v", vs, err)
	}
	if err := s.RefreshAPIs(ctx); err != nil {
		t.Fatal(err)
	}
}
