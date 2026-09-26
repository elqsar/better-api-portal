package devoidc

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func stub(t *testing.T) (*httptest.Server, *Provider) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	p, err := New("http://"+srv.Listener.Addr().String()+"/dev/oidc", []string{"eng-orders"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = p.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, p
}

func authz(srv *httptest.Server, verifier string) url.Values {
	sum := sha256.Sum256([]byte(verifier))
	return url.Values{"client_id": {ClientID}, "response_type": {"code"}, "state": {"s"}, "nonce": {"n"},
		"redirect_uri": {srv.URL + "/auth/callback"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"}, "name": {"Ada"}, "groups": {"eng-orders", "eng-invented"}}
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func code(t *testing.T, srv *httptest.Server, form url.Values) string {
	t.Helper()
	resp, err := noRedirect.PostForm(srv.URL+"/dev/oidc/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusSeeOther || loc.Query().Get("state") != "s" {
		t.Fatalf("authorize: %d %s", resp.StatusCode, loc)
	}
	return loc.Query().Get("code")
}

func exchange(t *testing.T, srv *httptest.Server, code, verifier string) int {
	t.Helper()
	resp, err := http.PostForm(srv.URL+"/dev/oidc/token", url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {srv.URL + "/auth/callback"}, "code_verifier": {verifier}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestToken(t *testing.T) {
	srv, p := stub(t)
	c := code(t, srv, authz(srv, "right"))
	if g := p.codes[c]; strings.Join(g.claims["groups"].([]string), ",") != "eng-orders" {
		t.Errorf("groups not offered were granted: %v", g.claims["groups"])
	}
	if s := exchange(t, srv, c, "right"); s != 200 {
		t.Errorf("exchange: %d", s)
	}
	if s := exchange(t, srv, c, "right"); s != 400 {
		t.Errorf("a code works twice: %d", s)
	}
	if s := exchange(t, srv, code(t, srv, authz(srv, "right")), "wrong"); s != 400 {
		t.Errorf("wrong PKCE verifier: %d", s)
	}
}

func TestAuthorizeChecks(t *testing.T) {
	srv, _ := stub(t)
	for name, edit := range map[string]func(url.Values){
		"foreign redirect": func(f url.Values) { f.Set("redirect_uri", "https://evil.example/cb") },
		"no PKCE":          func(f url.Values) { f.Del("code_challenge") },
		"other client":     func(f url.Values) { f.Set("client_id", "x") },
	} {
		f := authz(srv, "v")
		edit(f)
		resp, err := noRedirect.PostForm(srv.URL+"/dev/oidc/authorize", f)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
}
