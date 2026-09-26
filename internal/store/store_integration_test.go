//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"better-api-portal/internal/model"
)

var (
	templateOnce sync.Once
	templateErr  error
	dbSeq        atomic.Int64
)

const templateDB = "portal_test_template"

// testStore returns a store on a fresh database, cloned from a migrated
// template, and drops it when the test ends.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("PORTAL_TEST_DSN")
	if dsn == "" {
		t.Skip("PORTAL_TEST_DSN not set; run task dev:db and task test:integration")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	templateOnce.Do(func() { templateErr = makeTemplate(ctx, admin, dsn) })
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	name := fmt.Sprintf("portal_test_%d_%d", os.Getpid(), dbSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE "+templateDB); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, withDB(t, dsn, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		c, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	})
	return s
}

func makeTemplate(ctx context.Context, admin *pgx.Conn, dsn string) error {
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDB); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	u.Path = "/" + templateDB
	s, err := Open(ctx, u.String())
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Migrate(ctx)
}

func withDB(t *testing.T, dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func push(repo int64, semver, hash, status string) Push {
	return Push{
		API:    API{ID: "orders-http", Kind: "openapi", Owner: "team-orders", Lifecycle: "production"},
		RepoID: repo,
		Actor:  "repo:acme/orders",
		Version: Version{
			Semver: semver, ContentHash: hash, Status: status,
			Source: Source{Repo: "acme/orders", PushedBy: "ci", PushedAt: time.Unix(0, 0).UTC()},
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

	for _, p := range []Push{
		push(repo, "1.2.0", "sha256:a", StatusPublished),
		push(repo, "1.10.0", "sha256:b", StatusPublished),
		push(repo, "2.0.0-rc.1", "sha256:c", StatusPublished),
		push(repo, "3.0.0", "sha256:d", StatusRejected),
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
	if _, err := s.Record(ctx, push(repo, "1.2.0", "sha256:x", StatusPublished)); !errors.Is(err, ErrVersionExists) {
		t.Fatalf("republish: err = %v, want ErrVersionExists", err)
	}
	if _, err := s.Record(ctx, push(repo, "3.0.0", "sha256:d", StatusPublished)); err != nil {
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

	if owner, _ := s.ClaimOwner(ctx, "orders-http"); owner != "" {
		t.Fatalf("unclaimed owner = %q", owner)
	}
	if _, err := s.Record(ctx, push(orders, "1.0.0", "sha256:a", StatusPublished)); err != nil {
		t.Fatal(err)
	}
	if owner, _ := s.ClaimOwner(ctx, "orders-http"); owner != "acme/orders" {
		t.Fatalf("owner = %q", owner)
	}
	if _, err := s.Record(ctx, push(other, "1.1.0", "sha256:b", StatusPublished)); !errors.Is(err, ErrClaimed) {
		t.Fatalf("push from another repo: err = %v, want ErrClaimed", err)
	}

	// A rejected push leaves the metadata alone; a published one updates it.
	p := push(orders, "1.1.0", "sha256:b", StatusRejected)
	p.API.Lifecycle = "deprecated"
	if _, err := s.Record(ctx, p); err != nil {
		t.Fatal(err)
	}
	if lc := lifecycle(t, s); lc != "production" {
		t.Fatalf("lifecycle after rejected push = %q", lc)
	}
	p.Version.Status = StatusPublished
	if _, err := s.Record(ctx, p); err != nil {
		t.Fatal(err)
	}
	if lc := lifecycle(t, s); lc != "deprecated" {
		t.Fatalf("lifecycle after published push = %q", lc)
	}
}

func lifecycle(t *testing.T, s *Store) string {
	var lc string
	if err := s.pool.QueryRow(context.Background(), `SELECT lifecycle FROM apis WHERE id = 'orders-http'`).Scan(&lc); err != nil {
		t.Fatal(err)
	}
	return lc
}

func TestSyncTeams(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.SyncTeams(ctx, []Team{{"team-orders", "Orders", "eng-orders"}, {"team-gone", "Gone", ""}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncTeams(ctx, []Team{{"team-orders", "Orders!", "eng-orders"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	var name string
	s.pool.QueryRow(ctx, `SELECT count(*), max(name) FROM teams`).Scan(&n, &name)
	if n != 1 || name != "Orders!" {
		t.Fatalf("teams: %d, %q", n, name)
	}
}
