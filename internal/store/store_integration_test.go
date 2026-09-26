//go:build integration

package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"better-api-portal/internal/model"
	"better-api-portal/internal/store"
	"better-api-portal/internal/store/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

var testStore = storetest.New

func push(repo int64, semver, hash, status string) store.Push {
	return store.Push{
		API:    store.API{ID: "orders-http", Kind: "openapi", Owner: "team-orders", Lifecycle: "production"},
		RepoID: repo,
		Actor:  "repo:acme/orders",
		Version: store.Version{
			Semver: semver, ContentHash: hash, Status: status,
			Source: store.Source{Repo: "acme/orders", PushedBy: "ci", PushedAt: time.Unix(0, 0).UTC()},
		},
		Bundle:   []byte("tar.zst of " + hash),
		Score:    94,
		Findings: []model.Finding{{RuleID: "r1", Severity: model.SeverityWarn, Message: "m"}},
	}
}

func TestMigrateTwice(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestRecordAndLatest(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	repo, err := s.Repo(ctx, "acme/orders")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Repo(ctx, "acme/orders"); again != repo {
		t.Fatalf("Repo not idempotent: %d then %d", repo, again)
	}

	for _, p := range []store.Push{
		push(repo, "1.2.0", "sha256:a", store.StatusPublished),
		push(repo, "1.10.0", "sha256:b", store.StatusPublished),
		push(repo, "2.0.0-rc.1", "sha256:c", store.StatusPublished),
		push(repo, "3.0.0", "sha256:d", store.StatusRejected),
	} {
		p.Version.Prerelease = p.Version.Semver == "2.0.0-rc.1"
		if _, err := s.Record(ctx, p); err != nil {
			t.Fatalf("%s: %v", p.Version.Semver, err)
		}
	}
	latest, err := s.LatestPublished(ctx, "orders-http")
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.Semver != "1.10.0" {
		t.Fatalf("latest = %+v, want 1.10.0 (semver order, no pre-release, no rejected)", latest)
	}
	if b, _ := s.Bundle(ctx, latest.ContentHash); string(b) != "tar.zst of sha256:b" {
		t.Fatalf("bundle = %q", b)
	}
	score, fs, cs, err := s.Reports(ctx, latest.ID)
	if err != nil || score != 94 || len(fs) != 1 || cs != nil {
		t.Fatalf("reports = %d %v %v %v", score, fs, cs, err)
	}

	// Published versions are immutable; a rejected one can be retried.
	if _, err := s.Record(ctx, push(repo, "1.2.0", "sha256:x", store.StatusPublished)); !errors.Is(err, store.ErrVersionExists) {
		t.Fatalf("republish: err = %v, want ErrVersionExists", err)
	}
	if _, err := s.Record(ctx, push(repo, "3.0.0", "sha256:d", store.StatusPublished)); err != nil {
		t.Fatalf("publish after rejection: %v", err)
	}
	if v, _ := s.PublishedVersion(ctx, "orders-http", "3.0.0"); v == nil {
		t.Fatal("3.0.0 not published")
	}
}

func TestClaim(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	orders, _ := s.Repo(ctx, "acme/orders")
	other, _ := s.Repo(ctx, "acme/other")

	if a, _ := s.API(ctx, "orders-http"); a != nil {
		t.Fatalf("unclaimed api = %+v", a)
	}
	// Only a successful push claims an id.
	if _, err := s.Record(ctx, push(orders, "0.9.0", "sha256:z", store.StatusRejected)); !errors.Is(err, store.ErrUnclaimed) {
		t.Fatalf("rejected first push: err = %v, want ErrUnclaimed", err)
	}
	if a, _ := s.API(ctx, "orders-http"); a != nil {
		t.Fatalf("a rejected push claimed the api: %+v", a)
	}
	if _, err := s.Record(ctx, push(orders, "1.0.0", "sha256:a", store.StatusPublished)); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.API(ctx, "orders-http"); a == nil || a.Repo != "acme/orders" {
		t.Fatalf("api = %+v", a)
	}
	for _, status := range []string{store.StatusPublished, store.StatusRejected} {
		if _, err := s.Record(ctx, push(other, "1.1.0", "sha256:b", status)); !errors.Is(err, store.ErrClaimed) {
			t.Fatalf("%s push from another repo: err = %v, want ErrClaimed", status, err)
		}
	}

	// A rejected push leaves the metadata alone; a published one updates it.
	p := push(orders, "1.1.0", "sha256:b", store.StatusRejected)
	p.API.Lifecycle = "deprecated"
	if _, err := s.Record(ctx, p); err != nil {
		t.Fatal(err)
	}
	if lc := lifecycle(t, s); lc != "production" {
		t.Fatalf("lifecycle after rejected push = %q", lc)
	}
	p.Version.Status = store.StatusPublished
	if _, err := s.Record(ctx, p); err != nil {
		t.Fatal(err)
	}
	if lc := lifecycle(t, s); lc != "deprecated" {
		t.Fatalf("lifecycle after published push = %q", lc)
	}
}

func lifecycle(t *testing.T, s *store.Store) string {
	a, err := s.API(context.Background(), "orders-http")
	if err != nil || a == nil {
		t.Fatalf("api: %+v, %v", a, err)
	}
	return a.Lifecycle
}

func TestSyncTeams(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.SyncTeams(ctx, []store.Team{{"team-orders", "Orders", "eng-orders"}, {"team-gone", "Gone", ""}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncTeams(ctx, []store.Team{{"team-orders", "Orders!", "eng-orders"}}); err != nil {
		t.Fatal(err)
	}
	teams, err := s.Teams(ctx)
	if err != nil || len(teams) != 1 || teams[0].Name != "Orders!" {
		t.Fatalf("teams = %+v, %v", teams, err)
	}
}
