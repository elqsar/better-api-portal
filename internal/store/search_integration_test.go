//go:build integration

package store_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elqsar/better-api-portal/internal/store"
	"github.com/elqsar/better-api-portal/internal/store/storetest"
)

func search(t *testing.T, s *store.Store, sq store.SearchQuery) []store.Hit {
	t.Helper()
	if sq.Limit == 0 {
		sq.Limit = 200
	}
	hits, err := s.Search(context.Background(), sq)
	if err != nil {
		t.Fatalf("search %+v: %v", sq, err)
	}
	return hits
}

func TestSearch(t *testing.T) {
	s := testStore(t)
	storetest.SeedAPIs(t, s, 50)

	hits := search(t, s, store.SearchQuery{Q: "refund"})
	kinds := map[string]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
		if h.Semver != "1.1.0" {
			t.Errorf("hit from a version that isn't latest: %+v", h)
		}
		if h.Lifecycle == "retired" {
			t.Errorf("hit from a retired API: %+v", h)
		}
		if strings.Count(h.Snippet, store.SnippetStart) != strings.Count(h.Snippet, store.SnippetStop) {
			t.Errorf("unbalanced snippet %q", h.Snippet)
		}
	}
	for _, k := range []string{"api", "operation", "message", "schema"} {
		if !kinds[k] {
			t.Errorf("refund: no %s hit in %d hits", k, len(hits))
		}
	}
	// The UI groups by API, in the order of each API's best hit.
	if hits[0].APIID != "refunds-2" {
		t.Errorf("refund: first hit %+v, want one of the refunds API", hits[0])
	}
	if !slices.ContainsFunc(hits, func(h store.Hit) bool { return strings.Contains(h.Snippet, store.SnippetStart+"refund") }) {
		t.Error("refund: no snippet marks the word")
	}

	// A typo still finds titles, by trigram.
	typo := search(t, s, store.SearchQuery{Q: "refnd"})
	if !slices.ContainsFunc(typo, func(h store.Hit) bool { return strings.Contains(h.Title, "refund") }) {
		t.Errorf("refnd: no title with refund in %d hits", len(typo))
	}
	// Short queries match substrings of titles.
	if short := search(t, s, store.SearchQuery{Q: "v2"}); len(short) == 0 {
		t.Error("v2: no hits")
	}
	// Stop words alone make an empty tsquery, which isn't an error.
	search(t, s, store.SearchQuery{Q: "the"})
	if hits := search(t, s, store.SearchQuery{Q: "  "}); hits != nil {
		t.Errorf("blank query: %d hits", len(hits))
	}

	// Filters.
	for _, h := range search(t, s, store.SearchQuery{Q: "refund", Kind: "schema"}) {
		if h.Kind != "schema" {
			t.Errorf("kind filter: %+v", h)
		}
	}
	for _, h := range search(t, s, store.SearchQuery{Q: "refund", Team: "team-orders"}) {
		if h.Owner != "team-orders" {
			t.Errorf("team filter: %+v", h)
		}
	}
	if one := search(t, s, store.SearchQuery{Q: "refund", API: "refunds-2"}); len(one) == 0 ||
		slices.ContainsFunc(one, func(h store.Hit) bool { return h.APIID != "refunds-2" }) {
		t.Errorf("api filter: %+v", one)
	}
}

// An identical deprecated copy ranks at half weight; a retired one is
// hidden.
func TestSearchLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	idx := eventsIndex(t)
	for i, lc := range []string{"production", "deprecated", "retired"} {
		repo, _ := s.Repo(ctx, "acme/"+lc)
		p := eventsPush(repo, "1.0.0", fmt.Sprint("sha256:", i), store.StatusPublished, idx)
		p.API.ID, p.API.Lifecycle = "orders-"+lc, lc
		if _, err := s.Record(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rank := map[string]float32{}
	for _, h := range search(t, s, store.SearchQuery{Q: "cancelled", Kind: "message"}) {
		rank[h.APIID] = h.Rank
	}
	if _, ok := rank["orders-retired"]; ok {
		t.Error("retired API in results")
	}
	if p, d := rank["orders-production"], rank["orders-deprecated"]; p == 0 || d != p/2 {
		t.Errorf("ranks: production %v, deprecated %v", p, d)
	}
}

var latencyQueries = []string{"refund", "order cancelled", "refnd", "ref", "payment failed -legacy",
	"customer", "idempotent", "com.acme.orders", "RefundV3", "v1", "the", "journal entry archived"}

// J3: search across all APIs returns in < 300 ms at 500 APIs (p95).
func TestSearchLatency500(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 500 APIs")
	}
	ctx := context.Background()
	s := testStore(t)
	start := time.Now()
	storetest.SeedAPIs(t, s, 500)
	t.Logf("seeded 500 APIs in %v", time.Since(start).Round(time.Millisecond))

	var took []time.Duration
	slowest := map[string]time.Duration{}
	for range 5 {
		for _, q := range latencyQueries {
			start := time.Now()
			search(t, s, store.SearchQuery{Q: q, Limit: 200})
			d := time.Since(start)
			took = append(took, d)
			slowest[q] = max(slowest[q], d)
		}
	}
	for _, q := range latencyQueries {
		t.Logf("%-24q %v", q, slowest[q].Round(time.Millisecond))
	}
	slices.Sort(took)
	p95 := took[len(took)*95/100]
	t.Logf("%d searches: p50 %v, p95 %v, max %v", len(took), took[len(took)/2], p95, took[len(took)-1])
	if p95 >= 300*time.Millisecond {
		plan, _ := s.ExplainSearch(ctx, store.SearchQuery{Q: "refund", Limit: 200})
		t.Errorf("p95 %v, want < 300ms; plan for refund:\n%s", p95, plan)
	}
}

func BenchmarkSearch(b *testing.B) {
	ctx := context.Background()
	s := storetest.New(b)
	storetest.SeedAPIs(b, s, 500)
	if plan, err := s.ExplainSearch(ctx, store.SearchQuery{Q: "refund", Limit: 200}); err == nil {
		b.Logf("plan for refund:\n%s", plan)
	}
	for _, q := range []string{"refund", "refnd", "v1", "payment failed -legacy"} {
		b.Run(q, func(b *testing.B) {
			for b.Loop() {
				if _, err := s.Search(ctx, store.SearchQuery{Q: q, Limit: 200}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
