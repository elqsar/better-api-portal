//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/config"
	"better-api-portal/internal/httpapi"
	"better-api-portal/internal/store"
	"better-api-portal/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

// portal serves the REST API with real authentication over a fresh store.
func portal(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	cfg, err := config.Load(filepath.Join(example, "../portal.config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.PublicURL = "https://portal.test"
	s := storetest.New(t)
	api := &httpapi.Server{Store: s, Config: cfg, Auth: auth.NewCI(nil, s, nil),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	t.Setenv("PORTAL_URL", srv.URL)
	return srv, s
}

func token(t *testing.T, s *store.Store, repo string) (string, int64) {
	t.Helper()
	tok, hash := auth.NewToken()
	id, err := s.CreateToken(context.Background(), repo, hash, time.Now().Add(time.Hour), "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok, id
}

// checkout copies the example and applies edits (file, old, new).
func checkout(t *testing.T, edits ...[3]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	for _, e := range edits {
		p := filepath.Join(dir, e[0])
		b, _ := os.ReadFile(p)
		s := strings.Replace(string(b), e[1], e[2], 1)
		if s == string(b) {
			t.Fatalf("%s: %q not found", e[0], e[1])
		}
		os.WriteFile(p, []byte(s), 0o644)
	}
	return filepath.Join(dir, "portal.yaml")
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

// TestEndToEnd is M2's journey: publish, a no-op retry, a breaking change
// caught by check and rejected by push, then acknowledged and published.
func TestEndToEnd(t *testing.T) {
	srv, s := portal(t)
	tok, _ := token(t, s, "acme/orders")
	t.Setenv("PORTAL_TOKEN", tok)

	head := checkout(t)
	out, err := run("push", "--descriptor", head)
	if err != nil {
		t.Fatalf("first push: %v\n%s", err, out)
	}
	mustContain(t, out, "orders-http 2.3.0: published  https://portal.test/apis/orders-http/versions/2.3.0",
		"orders-events 1.4.0: published")
	// Published versions are indexed for the UI and search.
	if hits, err := s.Search(context.Background(), store.SearchQuery{Q: "cancel", Limit: 10}); err != nil || len(hits) == 0 {
		t.Errorf("search after push: %v, %v", hits, err)
	}

	out, err = run("push", "--descriptor", head)
	if err != nil {
		t.Fatalf("retry: %v\n%s", err, out)
	}
	mustContain(t, out, "orders-http 2.3.0: unchanged", "orders-events 1.4.0: unchanged")

	breaking := checkout(t,
		[3]string{"api/openapi.yaml", "enum: [customer_request, payment_failed, out_of_stock]", "enum: [customer_request, payment_failed]"},
		[3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.4.0"})
	out, err = run("check", "--descriptor", breaking, "--baseline-from", srv.URL)
	if !errors.Is(err, errFindings) {
		t.Fatalf("check: %v\n%s", err, out)
	}
	id := regexp.MustCompile(`BRK-OA-[0-9a-f]{6}`).FindString(out)
	if id == "" {
		t.Fatalf("no breaking change id:\n%s", out)
	}

	out, err = run("push", "--descriptor", breaking, "--output", "sarif="+filepath.Join(t.TempDir(), "p.sarif"))
	if !errors.Is(err, errFindings) {
		t.Fatalf("breaking push: %v\n%s", err, out)
	}
	mustContain(t, out, id, "orders-http 2.4.0: rejected", "orders-events 1.4.0: unchanged")

	out, err = run("push", "--descriptor", breaking, "--ack", id, "--ack-reason", "nobody sends out_of_stock")
	if err != nil {
		t.Fatalf("acknowledged push: %v\n%s", err, out)
	}
	mustContain(t, out, "orders-http 2.4.0: published")

	// The new version is now the baseline.
	out, err = run("check", "--descriptor", breaking, "--baseline-from", srv.URL)
	if err != nil {
		t.Fatalf("check after publish: %v\n%s", err, out)
	}
	mustContain(t, out, "orders-http 2.4.0 (baseline 2.4.0, 0 change(s))")
}

func TestEndToEndAuth(t *testing.T) {
	_, s := portal(t)
	owner, ownerID := token(t, s, "acme/orders")
	t.Setenv("PORTAL_TOKEN", owner)
	if out, err := run("push", "--descriptor", checkout(t)); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	// Another repo can't take the ids.
	other, _ := token(t, s, "acme/fork")
	t.Setenv("PORTAL_TOKEN", other)
	out, err := run("push", "--descriptor", checkout(t, [3]string{"api/openapi.yaml", "version: 2.3.0", "version: 2.3.1"}))
	if !errors.Is(err, errFindings) {
		t.Fatalf("fork push: %v\n%s", err, out)
	}
	mustContain(t, out, "[api-claimed]", "orders-http 2.3.1: rejected")

	// A revoked token is refused outright.
	if err := s.RevokeToken(context.Background(), ownerID, "test"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORTAL_TOKEN", owner)
	if _, err := run("push", "--descriptor", checkout(t)); err == nil || errors.Is(err, errFindings) || !strings.Contains(err.Error(), "401") {
		t.Errorf("revoked token: %v", err)
	}
}
