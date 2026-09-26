// Package devoidc is a stub OIDC identity provider for local development
// and tests: `portal serve --dev-login` mounts it at /dev/oidc, and its
// sign-in page lets anyone be anyone, with any of the configured groups.
// It is never for production: it has no passwords.
package devoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ClientID is the client id the stub expects.
const ClientID = "portal-dev"

// Provider is the stub. Its issuer is a URL on the portal itself.
type Provider struct {
	issuer *url.URL
	groups []string // offered on the sign-in page
	key    *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	redirectURI, challenge, nonce string
	claims                        map[string]any
	expires                       time.Time
}

// New returns a stub whose issuer is issuer, offering the groups.
func New(issuer string, groups []string) (*Provider, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Provider{issuer: u, groups: groups, key: key, codes: map[string]grant{}}, nil
}

// Issuer is the issuer URL to configure as oidc.issuer.
func (p *Provider) Issuer() string { return p.issuer.String() }

// Handler serves the provider's endpoints under the issuer's path.
func (p *Provider) Handler() http.Handler {
	base := strings.TrimSuffix(p.issuer.Path, "/")
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+base+"/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET "+base+"/authorize", p.authorizePage)
	mux.HandleFunc("POST "+base+"/authorize", p.authorize)
	mux.HandleFunc("POST "+base+"/token", p.token)
	mux.HandleFunc("GET "+base+"/keys", p.keys)
	return mux
}

func (p *Provider) endpoint(name string) string { return p.issuer.JoinPath(name).String() }

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.Issuer(),
		"authorization_endpoint":                p.endpoint("authorize"),
		"token_endpoint":                        p.endpoint("token"),
		"jwks_uri":                              p.endpoint("keys"),
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &p.key.PublicKey, KeyID: "dev", Algorithm: "RS256", Use: "sig"}}})
}

var page = template.Must(template.New("").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Development sign-in</title><link rel="stylesheet" href="/static/app.css"></head>
<body><main><div class="narrow panel">
<h1>Development sign-in</h1>
<p class="muted">This portal runs with <code>--dev-login</code>: anyone can sign in as anyone. Never use it in production.</p>
<form method="post">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<div class="field"><label for="name">Name</label><input type="text" id="name" name="name" value="Dev User" required></div>
<div class="field"><label for="email">Email</label><input type="email" id="email" name="email" value="dev@example.com"></div>
{{if .Groups}}<div class="field checks"><label>Groups</label>
{{range .Groups}}<label><input type="checkbox" name="groups" value="{{.}}" checked> {{.}}</label>{{end}}</div>{{end}}
<button class="primary" type="submit">Sign in</button>
</form></div></main></body></html>`))

// params are the authorization request's parameters, carried through the
// sign-in form.
var params = []string{"client_id", "redirect_uri", "state", "nonce", "code_challenge", "code_challenge_method", "response_type", "scope"}

func (p *Provider) authorizePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := p.checkRequest(q); err != "" {
		http.Error(w, err, http.StatusBadRequest)
		return
	}
	hidden := map[string]string{}
	for _, k := range params {
		hidden[k] = q.Get(k)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; form-action 'self'")
	page.Execute(w, map[string]any{"Hidden": hidden, "Groups": p.groups})
}

// checkRequest validates an authorization request. Only redirects back to
// the portal itself are allowed, so the stub can't send codes elsewhere.
func (p *Provider) checkRequest(q url.Values) string {
	if q.Get("client_id") != ClientID {
		return "unknown client_id"
	}
	if q.Get("response_type") != "code" {
		return "response_type must be code"
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return "PKCE (S256) is required"
	}
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || u.Scheme != p.issuer.Scheme || u.Host != p.issuer.Host {
		return "redirect_uri must be on " + p.issuer.Host
	}
	return ""
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := r.PostForm
	if err := p.checkRequest(f); err != "" {
		http.Error(w, err, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(f.Get("name"))
	if name == "" {
		http.Error(w, "a name is required", http.StatusBadRequest)
		return
	}
	var groups []string
	for _, g := range f["groups"] {
		for _, offered := range p.groups {
			if g == offered {
				groups = append(groups, g)
			}
		}
	}
	code := random()
	p.mu.Lock()
	for c, g := range p.codes {
		if time.Now().After(g.expires) {
			delete(p.codes, c)
		}
	}
	p.codes[code] = grant{
		redirectURI: f.Get("redirect_uri"), challenge: f.Get("code_challenge"), nonce: f.Get("nonce"),
		claims: map[string]any{
			"sub": "dev:" + strings.ToLower(strings.Join(strings.Fields(name), ".")), "name": name,
			"email": f.Get("email"), "groups": groups,
		},
		expires: time.Now().Add(time.Minute),
	}
	p.mu.Unlock()
	to, _ := url.Parse(f.Get("redirect_uri"))
	q := to.Query()
	q.Set("code", code)
	q.Set("state", f.Get("state"))
	to.RawQuery = q.Encode()
	http.Redirect(w, r, to.String(), http.StatusSeeOther)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	f := r.PostForm
	p.mu.Lock()
	g, ok := p.codes[f.Get("code")]
	delete(p.codes, f.Get("code")) // one use
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	switch {
	case f.Get("grant_type") != "authorization_code", !ok, time.Now().After(g.expires),
		f.Get("redirect_uri") != g.redirectURI,
		base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		tokenError(w, "invalid_grant")
		return
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "dev"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	idt, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: p.Issuer(), Audience: jwt.Audience{ClientID},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Claims(map[string]any{"nonce": g.nonce}).Claims(g.claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": random(), "token_type": "Bearer", "expires_in": 300, "id_token": idt,
	})
}

func tokenError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func random() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
