//go:build integration

// Package storetest gives integration tests a store on a fresh database.
// It needs PORTAL_TEST_DSN, which task test:integration sets.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"better-api-portal/internal/store"
)

var (
	templateOnce sync.Once
	templateErr  error
	dbSeq        atomic.Int64
)

// Each test package runs in its own process, so the template is per process.
var templateDB = fmt.Sprintf("portal_test_template_%d", os.Getpid())

// New returns a store on a database cloned from a migrated template, and
// drops the database when the test ends. It skips the test without
// PORTAL_TEST_DSN.
func New(t *testing.T) *store.Store {
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
	s, err := store.Open(ctx, withDB(dsn, name))
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
	s, err := store.Open(ctx, withDB(dsn, templateDB))
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Migrate(ctx)
}

func withDB(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Main runs the tests, then drops the template database. Call it from the
// package's TestMain.
func Main(m *testing.M) int {
	code := m.Run()
	if dsn := os.Getenv("PORTAL_TEST_DSN"); dsn != "" {
		ctx := context.Background()
		if c, err := pgx.Connect(ctx, dsn); err == nil {
			c.Exec(ctx, "DROP DATABASE IF EXISTS "+templateDB)
			c.Close(ctx)
		}
	}
	return code
}
