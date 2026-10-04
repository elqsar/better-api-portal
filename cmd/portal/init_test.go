package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/initkit"
)

func TestInit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "orders-service")
	if err := os.CopyFS(root, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "portal.yaml"))

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--root", root}, "--owner is required"},
		{[]string{"--root", root, "--owner", "team-orders", "--ci", "gitlab"}, `unknown --ci "gitlab"`},
		{[]string{"--root", root, "--owner", "team-orders", "--ci", "github"}, "--ci needs --portal-url"},
		{[]string{"--root", root, "--owner", "team-orders", "--agents"}, "--agents needs --portal-url"},
		{[]string{"--root", t.TempDir(), "--owner", "team-orders"}, "no OpenAPI 3"},
	} {
		if _, err := run(append([]string{"init"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.args, err, c.want)
		}
	}

	out, err := run("init", "--root", root, "--owner", "team-orders", "--stdout")
	if err != nil || !strings.Contains(out, "- id: orders-http") {
		t.Fatalf("--stdout: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "portal.yaml")); err == nil {
		t.Error("--stdout wrote portal.yaml")
	}

	args := []string{"init", "--root", root, "--owner", "team-orders", "--ci", "github",
		"--portal-url", "https://portal.test", "--cli-version", "v0.2.0"}
	out, err = run(args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{"wrote " + filepath.Join(root, "portal.yaml"), "0 error(s)", "orders-http 2.3.0: score"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	wf, err := os.ReadFile(filepath.Join(root, initkit.WorkflowPath))
	if err != nil || !strings.Contains(string(wf), "PORTAL_CLI_VERSION: v0.2.0") {
		t.Errorf("workflow: %v", err)
	}

	// Nothing is overwritten without --force.
	if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("rerun: %v", err)
	}
	if _, err := run(append(args, "--force")...); err != nil {
		t.Errorf("--force: %v", err)
	}

	// --agents adds a section to AGENTS.md, and a rerun replaces it.
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Orders\n\nRun task test.\n"), 0o644)
	for range 2 {
		if out, err := run(append(args, "--force", "--agents")...); err != nil || !strings.Contains(out, "added the API portal section") {
			t.Fatalf("--agents: %v\n%s", err, out)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if s := string(agents); !strings.HasPrefix(s, "# Orders\n\nRun task test.\n\n<!-- api-portal:start") ||
		strings.Count(s, "## API portal") != 1 || !strings.Contains(s, "https://portal.test/mcp") ||
		!strings.Contains(s, "- `orders-events`: `api/events.yaml`") {
		t.Errorf("AGENTS.md:\n%s", s)
	}
}

func TestInitEventsFrom(t *testing.T) {
	root := filepath.Join(t.TempDir(), "orders-service")
	if err := os.CopyFS(root, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "portal.yaml"))
	os.Remove(filepath.Join(root, "api/events.yaml"))

	out, err := run("init", "--root", root, "--owner", "team-orders", "--events-from", filepath.Join(root, "api/schemas"),
		"--type-prefix", "com.acme.orders.", "--kafka-topic", "orders.events")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, s := range []string{"wrote " + filepath.Join(root, "api/events.yaml"), "orders-events 1.0.0: score", "0 error(s)"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	desc, _ := os.ReadFile(filepath.Join(root, "portal.yaml"))
	if !strings.Contains(string(desc), "spec: api/events.yaml") {
		t.Errorf("portal.yaml:\n%s", desc)
	}

	// Outside the root.
	if _, err := run("init", "--root", root, "--owner", "team-orders", "--events-from", filepath.Join(root, "api/schemas"),
		"--events-out", filepath.Join(t.TempDir(), "events.yaml"), "--stdout"); err == nil || !strings.Contains(err.Error(), "inside the repository root") {
		t.Errorf("outside the root: %v", err)
	}
}
