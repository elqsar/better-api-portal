package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elqsar/better-api-portal/internal/bundle"
	"github.com/elqsar/better-api-portal/internal/httpapi"
)

// fakePortal answers pushes with resp and serves the example's orders-http
// as its latest version.
func fakePortal(t *testing.T, resp string) *httptest.Server {
	t.Helper()
	c, problems, err := bundle.Load(example, filepath.Join(example, "api/openapi.yaml"))
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	b, err := bundle.Read(c.Root, c.Files())
	if err != nil {
		t.Fatal(err)
	}
	var packed bytes.Buffer
	if err := b.Pack(&packed); err != nil {
		t.Fatal(err)
	}
	hash, _ := b.Hash()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ptk_test" {
			http.Error(w, `{"error":"unauthenticated"}`, 401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/push":
			if err := r.ParseMultipartForm(8 << 20); err != nil || len(r.MultipartForm.File) != 3 {
				http.Error(w, `{"error":"want a descriptor and two bundles"}`, 400)
				return
			}
			w.Write([]byte(resp))
		case "/api/v1/apis/orders-http/versions/latest/bundle":
			w.Header().Set(httpapi.HeaderVersion, "2.3.0")
			w.Header().Set(httpapi.HeaderContentHash, hash)
			w.Header().Set(httpapi.HeaderLifecycle, "production")
			w.Write(packed.Bytes())
		default:
			http.Error(w, `{"error":"not found"}`, 404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PORTAL_TOKEN", "ptk_test")
	return srv
}

func TestPush(t *testing.T) {
	srv := fakePortal(t, `{"findings": [{"api": "orders-http", "rule_id": "r", "severity": "error",
		"file": "api/openapi.yaml", "line": 3, "message": "bad"}],
		"apis": [{"id": "orders-http", "version": "2.4.0", "score": 90, "status": "rejected"},
		         {"id": "orders-events", "version": "1.4.0", "score": 98, "status": "published",
		          "url": "https://portal.test/apis/orders-events/versions/1.4.0"}]}`)
	out, err := run("push", "--url", srv.URL, "--descriptor", example+"/portal.yaml")
	if !errors.Is(err, errFindings) {
		t.Fatalf("err = %v; out:\n%s", err, out)
	}
	for _, want := range []string{
		example + "/api/openapi.yaml:3: error [r] bad", // relative to the working directory again
		"orders-http 2.4.0: rejected",
		"orders-events 1.4.0: published  https://portal.test/apis/orders-events/versions/1.4.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, err = run("push", "--url", srv.URL, "--descriptor", example+"/portal.yaml", "--format", "json")
	var resp httpapi.PushResponse
	if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil || len(resp.APIs) != 2 || resp.APIs[1].URL == "" {
		t.Errorf("json: %v, %v:\n%s", err, jerr, out)
	}
}

func TestPushErrors(t *testing.T) {
	srv := fakePortal(t, `{}`)
	if _, err := run("push", "--descriptor", example+"/portal.yaml"); err == nil || !strings.Contains(err.Error(), "PORTAL_URL") {
		t.Errorf("no URL: %v", err)
	}
	t.Setenv("PORTAL_TOKEN", "ptk_wrong")
	_, err := run("push", "--url", srv.URL, "--descriptor", example+"/portal.yaml")
	if err == nil || errors.Is(err, errFindings) || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: %v", err)
	}
	// A broken $ref fails before anything is sent.
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(example)); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(dir, "api/openapi.yaml")
	b, _ := os.ReadFile(spec)
	os.WriteFile(spec, append(b, []byte("x-broken: {$ref: './missing.yaml'}\n")...), 0o644)
	out, err := run("push", "--url", "http://127.0.0.1:1", "--descriptor", dir+"/portal.yaml")
	if !errors.Is(err, errFindings) || !strings.Contains(out, "bundle-ref") {
		t.Errorf("broken ref: %v:\n%s", err, out)
	}
}

func TestCheckBaselineFrom(t *testing.T) {
	srv := fakePortal(t, `{}`)
	out, err := run("check", "--descriptor", example+"/portal.yaml", "--baseline-from", srv.URL)
	if err != nil {
		t.Fatalf("err = %v:\n%s", err, out)
	}
	// orders-http is diffed against the portal's copy; orders-events has none.
	if !strings.Contains(out, "orders-http 2.3.0 (baseline 2.3.0, 0 change(s))") {
		t.Errorf("no baseline from the portal:\n%s", out)
	}
}
