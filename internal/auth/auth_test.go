package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"better-api-portal/internal/config"
	"better-api-portal/internal/httpapi"
	"better-api-portal/internal/store"
)

// issuerServer is a CI OIDC issuer: discovery, keys, and a signer.
type issuerServer struct {
	*httptest.Server
	key *rsa.PrivateKey
}

func newIssuer(t *testing.T) *issuerServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &issuerServer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": is.URL, "jwks_uri": is.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	is.Server = httptest.NewServer(mux)
	t.Cleanup(is.Close)
	return is
}

// sign returns an ID token with the standard claims for aud plus extra.
func (is *issuerServer) sign(t *testing.T, key *rsa.PrivateKey, aud string, exp time.Time, extra map[string]any) string {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	std := jwt.Claims{Issuer: is.URL, Subject: "repo:acme/orders:ref:refs/heads/main",
		Audience: jwt.Audience{aud}, IssuedAt: jwt.NewNumericDate(time.Now()), Expiry: jwt.NewNumericDate(exp)}
	tok, err := jwt.Signed(s).Claims(std).Claims(extra).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type fakeTokens map[string]*store.Token

func (f fakeTokens) TokenByHash(_ context.Context, hash []byte) (*store.Token, error) {
	return f[string(hash)], nil
}

func request(token string, headers ...string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/push", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestIDToken(t *testing.T) {
	is := newIssuer(t)
	gitlab := newIssuer(t)
	cfg := []config.TrustedIssuer{
		{Issuer: is.URL, Audience: "api-portal", RepoClaim: "repository", AllowedRefs: config.DefaultAllowedRefs},
		{Issuer: gitlab.URL, Audience: "api-portal", RepoClaim: "project_path", RepoPrefix: "gitlab:",
			AllowedRefs: config.DefaultAllowedRefs},
	}
	c := NewCI(cfg, fakeTokens{}, is.Client())
	hour := time.Now().Add(time.Hour)
	github := map[string]any{"repository": "acme/orders", "ref": "refs/heads/main", "sha": "abc123", "run_id": "42"}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	for name, tc := range map[string]struct {
		token string
		want  *httpapi.Identity
		err   string
	}{
		"main": {token: is.sign(t, is.key, "api-portal", hour, github), want: &httpapi.Identity{
			Repo: "acme/orders", Actor: "repo:acme/orders:ref:refs/heads/main", Ref: "refs/heads/main",
			Commit: "abc123", CanPush: true}},
		"pull request can't push": {
			token: is.sign(t, is.key, "api-portal", hour, map[string]any{"repository": "acme/orders", "ref": "refs/pull/7/merge"}),
			want:  &httpapi.Identity{Repo: "acme/orders", Actor: "repo:acme/orders:ref:refs/heads/main", Ref: "refs/pull/7/merge"}},
		"gitlab tag": {
			token: gitlab.sign(t, gitlab.key, "api-portal", hour, map[string]any{
				"project_path": "acme/orders", "ref": "v2.3.0", "ref_type": "tag", "pipeline_id": "9"}),
			want: &httpapi.Identity{Repo: "gitlab:acme/orders", Actor: "repo:acme/orders:ref:refs/heads/main",
				Ref: "refs/tags/v2.3.0", RunURL: gitlab.URL + "/acme/orders/-/pipelines/9", CanPush: true}},
		"wrong audience":   {token: is.sign(t, is.key, "https://github.com/acme", hour, github), err: "audience"},
		"expired":          {token: is.sign(t, is.key, "api-portal", time.Now().Add(-time.Minute), github), err: "expired"},
		"forged signature": {token: is.sign(t, other, "api-portal", hour, github), err: "signature"},
		"no repo claim":    {token: is.sign(t, is.key, "api-portal", hour, map[string]any{"ref": "refs/heads/main"}), err: `no "repository" claim`},
		"untrusted issuer": {token: "e30." + b64(`{"iss":"https://evil.example"}`) + ".sig", err: "not trusted"},
		"not a JWT":        {token: "hello", err: "malformed"},
		"no token":         {err: "Authorization: Bearer"},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := c.Authenticate(request(tc.token))
			if tc.err != "" {
				if !errors.Is(err, httpapi.ErrUnauthenticated) || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want unauthenticated with %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if *id != *tc.want {
				t.Errorf("identity = %+v\nwant       %+v", *id, *tc.want)
			}
		})
	}
}

func TestIssuerDown(t *testing.T) {
	is := newIssuer(t)
	tok := is.sign(t, is.key, "api-portal", time.Now().Add(time.Hour), map[string]any{"repository": "acme/orders"})
	url := is.URL
	is.Close()
	c := NewCI([]config.TrustedIssuer{{Issuer: url, Audience: "api-portal", RepoClaim: "repository"}}, fakeTokens{}, nil)
	// Not the caller's fault: a server error, not a 401.
	if _, err := c.Authenticate(request(tok)); err == nil || errors.Is(err, httpapi.ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaticToken(t *testing.T) {
	tok, hash := NewToken()
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) != len(TokenPrefix)+43 {
		t.Fatalf("token = %q", tok)
	}
	c := NewCI(nil, fakeTokens{string(hash): {ID: 7, Repo: "acme/legacy"}}, nil)
	id, err := c.Authenticate(request(tok, HeaderRef, "refs/heads/main", HeaderCommit, "abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := httpapi.Identity{Repo: "acme/legacy", Actor: "token:7", Ref: "refs/heads/main", Commit: "abc", CanPush: true}
	if *id != want {
		t.Errorf("identity = %+v", *id)
	}
	if other, _ := NewToken(); other == tok {
		t.Error("tokens repeat")
	}
	if _, err := c.Authenticate(request(TokenPrefix + "unknown")); !errors.Is(err, httpapi.ErrUnauthenticated) {
		t.Errorf("unknown token: %v", err)
	}
}

func TestQualifiedRef(t *testing.T) {
	for _, tc := range [][3]string{
		{"refs/heads/main", "branch", "refs/heads/main"},
		{"main", "branch", "refs/heads/main"},
		{"v1", "tag", "refs/tags/v1"},
		{"main", "", "main"},
	} {
		if got := qualifiedRef(tc[0], tc[1]); got != tc[2] {
			t.Errorf("qualifiedRef(%q, %q) = %q", tc[0], tc[1], got)
		}
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
