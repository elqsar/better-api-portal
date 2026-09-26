package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/bundle"
	"better-api-portal/internal/httpapi"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func testClient(t *testing.T, h http.HandlerFunc, creds *Credentials) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", creds)
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return c
}

func testBundle(t *testing.T) *bundle.Bundle {
	t.Helper()
	return &bundle.Bundle{Entry: "openapi.yaml", Files: map[string][]byte{"openapi.yaml": []byte("openapi: 3.1.0\n")}}
}

func TestFromEnv(t *testing.T) {
	if c := FromEnv(env(), "api-portal"); c != nil {
		t.Errorf("empty env: %+v", c)
	}
	c := FromEnv(env("PORTAL_TOKEN", "ptk_x", "ACTIONS_ID_TOKEN_REQUEST_URL", "https://gh"), "api-portal")
	if c.Token != "ptk_x" || c.githubURL != "" {
		t.Errorf("PORTAL_TOKEN should win: %+v", c)
	}
	gh := FromEnv(env("GITHUB_ACTIONS", "true", "GITHUB_REF", "refs/heads/main", "GITHUB_SHA", "abc",
		"GITHUB_SERVER_URL", "https://github.com", "GITHUB_REPOSITORY", "acme/orders", "GITHUB_RUN_ID", "42",
		"ACTIONS_ID_TOKEN_REQUEST_URL", "https://gh"), "api-portal")
	if gh.githubURL != "https://gh" || gh.Run[auth.HeaderRunURL] != "https://github.com/acme/orders/actions/runs/42" ||
		gh.Run[auth.HeaderRef] != "refs/heads/main" || gh.Run[auth.HeaderCommit] != "abc" {
		t.Errorf("github: %+v", gh)
	}
	gl := FromEnv(env("PORTAL_TOKEN", "jwt", "GITLAB_CI", "true", "CI_COMMIT_TAG", "v1.2.0"), "api-portal")
	if gl.Run[auth.HeaderRef] != "refs/tags/v1.2.0" {
		t.Errorf("gitlab: %+v", gl.Run)
	}
}

func TestGitHubIDToken(t *testing.T) {
	actions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime" || r.URL.Query().Get("audience") != "api-portal" ||
			r.URL.Query().Get("api-version") != "2.0" {
			http.Error(w, "bad request", 400)
			return
		}
		w.Write([]byte(`{"value": "the.id.token"}`))
	}))
	defer actions.Close()
	creds := FromEnv(env("GITHUB_ACTIONS", "true", "GITHUB_REF", "refs/heads/main",
		"ACTIONS_ID_TOKEN_REQUEST_URL", actions.URL+"?api-version=2.0",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runtime"), "api-portal")
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer the.id.token" {
			http.Error(w, `{"error":"wrong token"}`, 401)
			return
		}
		if r.Header.Get(auth.HeaderRef) != "" {
			http.Error(w, `{"error":"run headers are for static tokens"}`, 400)
			return
		}
		http.Error(w, `{"error":"none"}`, 404)
	}, creds)
	if b, _, err := c.Latest(context.Background(), "orders-http"); b != nil || err != nil {
		t.Fatalf("latest = %v, %v", b, err)
	}
}

func TestPushRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/push" || r.Header.Get("Authorization") != "Bearer ptk_x" ||
			r.Header.Get(auth.HeaderCommit) != "abc" {
			http.Error(w, `{"error":"unexpected request"}`, 400)
			return
		}
		if calls.Add(1) < 3 {
			http.Error(w, `{"error":"deploying"}`, 503)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if r.MultipartForm.File[httpapi.PartBundlePrefix+"orders-http"] == nil || r.MultipartForm.Value[httpapi.PartAcks] == nil {
			http.Error(w, `{"error":"missing parts"}`, 400)
			return
		}
		w.Write([]byte(`{"findings": [], "apis": [{"id": "orders-http", "status": "published"}]}`))
	}, &Credentials{Token: "ptk_x", Run: map[string]string{auth.HeaderCommit: "abc"}})
	resp, err := c.Push(context.Background(), []byte("apiVersion: portal/v1\n"),
		map[string]*bundle.Bundle{"orders-http": testBundle(t)}, map[string]string{"BRK-OA-1": "why"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || resp.APIs[0].Status != "published" {
		t.Errorf("calls = %d, resp = %+v", calls.Load(), resp)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"no bearer token"}`, 401)
	}, &Credentials{Token: "x"})
	_, err := c.Push(context.Background(), nil, nil, nil, true)
	var e *Error
	if !errors.As(err, &e) || e.Status != 401 || !strings.Contains(err.Error(), "id-token: write") || calls.Load() != 1 {
		t.Errorf("err = %v after %d call(s)", err, calls.Load())
	}
	c.Credentials = nil
	if _, err := c.Push(context.Background(), nil, nil, nil, true); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("no credentials: %v", err)
	}
}

func TestLatestVerifiesHash(t *testing.T) {
	b := testBundle(t)
	var packed bytes.Buffer
	if err := b.Pack(&packed); err != nil {
		t.Fatal(err)
	}
	hash, _ := b.Hash()
	serve := func(h string) *Client {
		return testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(httpapi.HeaderVersion, "2.3.0")
			w.Header().Set(httpapi.HeaderLifecycle, "production")
			w.Header().Set(httpapi.HeaderContentHash, h)
			w.Write(packed.Bytes())
		}, &Credentials{Token: "x"})
	}
	base, v, err := serve(hash).Latest(context.Background(), "orders-http")
	if err != nil || v != "2.3.0" || base.Lifecycle != "production" || base.Bundle.Entry != "openapi.yaml" {
		t.Fatalf("latest = %+v %q %v", base, v, err)
	}
	if _, _, err := serve("sha256:other").Latest(context.Background(), "orders-http"); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("tampered bundle: %v", err)
	}
}
