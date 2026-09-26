package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
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

func TestCheckSARIF(t *testing.T) {
	// From the repository root, as in CI, so locations are relative to it.
	t.Chdir("../..")
	out, err := run("check", "--descriptor", "docs/spec/examples/orders-service/portal.yaml", "--format", "sarif")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	var log struct {
		Runs []struct {
			Results []struct {
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string }
					}
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	results := log.Runs[0].Results
	if len(results) != 4 {
		t.Errorf("%d results, want the example's 4 warnings", len(results))
	}
	if uri := results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI; uri != "docs/spec/examples/orders-service/api/openapi.yaml" {
		t.Errorf("uri = %q", uri)
	}
}

func TestCheckOutputs(t *testing.T) {
	// A failing run: a breaking change against a bundle baseline.
	dir := t.TempDir()
	if _, err := run("bundle", "--descriptor", example+"/portal.yaml", "--out", dir); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(dir, "head")
	if err := os.CopyFS(head, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(head, "api", "openapi.yaml")
	b, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(strings.Replace(string(b), ", out_of_stock]", "]", 1), "version: 2.3.0", "version: 2.4.0", 1)
	if err := os.WriteFile(spec, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	sarif, junit := filepath.Join(dir, "r.sarif"), filepath.Join(dir, "r.xml")
	out, err := run("check", "--descriptor", filepath.Join(head, "portal.yaml"),
		"--baseline", "orders-http="+filepath.Join(dir, "orders-http.tar.zst"),
		"--output", "sarif="+sarif, "--output", "junit="+junit)
	if !errors.Is(err, errFindings) || !strings.Contains(out, "error(s)") {
		t.Fatalf("err = %v, want failing findings with text on stdout\n%s", err, out)
	}
	sb, err := os.ReadFile(sarif)
	if err != nil || !json.Valid(sb) || !strings.Contains(string(sb), "BRK-OA-") {
		t.Errorf("sarif file: err = %v\n%s", err, sb)
	}
	var suites struct {
		Failures int `xml:"failures,attr"`
	}
	jb, err := os.ReadFile(junit)
	if err != nil || xml.Unmarshal(jb, &suites) != nil || suites.Failures != 1 {
		t.Errorf("junit file: err = %v, failures = %d\n%s", err, suites.Failures, jb)
	}
}

func TestCheckOutputUsage(t *testing.T) {
	for _, bad := range [][]string{
		{"--output", "bogus=x"},
		{"--output", "sarif="},
		{"--output", "sarif=a", "--output", "junit=a"},
		{"--format", "xml"},
	} {
		args := append([]string{"check", "--descriptor", example + "/portal.yaml"}, bad...)
		if out, err := run(args...); err == nil || errors.Is(err, errFindings) || out != "" {
			t.Errorf("%v: err = %v, out = %q; want a usage error before running", bad, err, out)
		}
	}
}
