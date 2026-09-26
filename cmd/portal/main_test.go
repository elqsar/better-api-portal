package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const example = "../../docs/spec/examples/orders-service"

func run(args ...string) (string, error) {
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestAckNeedsReason(t *testing.T) {
	_, err := run("check", "--descriptor", example+"/portal.yaml", "--ack", "BRK-CE-000000")
	if err == nil || errors.Is(err, errFindings) || !strings.Contains(err.Error(), "--ack-reason") {
		t.Errorf("err = %v", err)
	}
}

func TestDiffExitCodes(t *testing.T) {
	out, err := run("diff", example+"/api/events.yaml", example+"/api/events.yaml")
	if err != nil || strings.TrimSpace(out) != "no changes" {
		t.Errorf("identical: out = %q, err = %v", out, err)
	}
	if _, err := run("diff", "--compatibility", "sideways", example+"/api/events.yaml", example+"/api/events.yaml"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

func TestDiffOpenAPI(t *testing.T) {
	spec := example + "/api/openapi.yaml"
	out, err := run("diff", spec, spec)
	if err != nil || strings.TrimSpace(out) != "no changes" {
		t.Errorf("identical: out = %q, err = %v", out, err)
	}

	b, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), ", out_of_stock]", "]", 1)
	if edited == string(b) {
		t.Fatal("fixture edit didn't apply")
	}
	dir := filepath.Join(t.TempDir(), "api")
	if err := os.CopyFS(dir, os.DirFS(example+"/api")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run("diff", spec, filepath.Join(dir, "openapi.yaml"))
	if !errors.Is(err, errFindings) || !strings.Contains(out, "BRK-OA-") {
		t.Errorf("breaking: out = %q, err = %v", out, err)
	}
}

func TestBundleThenCheck(t *testing.T) {
	out := t.TempDir()
	stdout, err := run("bundle", "--descriptor", example+"/portal.yaml", "--out", out)
	if err != nil {
		t.Fatalf("bundle: %v\n%s", err, stdout)
	}
	for _, id := range []string{"orders-http", "orders-events"} {
		if !strings.Contains(stdout, id+"  sha256:") {
			t.Errorf("no hash printed for %s:\n%s", id, stdout)
		}
	}
	stdout, err = run("check", "--descriptor", example+"/portal.yaml",
		"--baseline", "orders-http="+filepath.Join(out, "orders-http.tar.zst"),
		"--baseline", "orders-events="+filepath.Join(out, "orders-events.tar.zst"))
	if err != nil || !strings.Contains(stdout, "orders-http 2.3.0 (baseline 2.3.0, 0 change(s))") {
		t.Errorf("check: err = %v\n%s", err, stdout)
	}
	if _, err := run("bundle", "--descriptor", example+"/portal.yaml"); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("no --out: err = %v", err)
	}
}
