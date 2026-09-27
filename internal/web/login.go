package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/elqsar/better-api-portal/internal/store"
)

// Cookie names.
const (
	sessionCookie = "portal_session"
	// loginCookie plus a prefix of the state holds a sign-in in progress:
	// one cookie per sign-in, so parallel ones (two tabs, or a stray
	// request sent to sign in) don't overwrite each other.
	loginCookie = "portal_login_"
)

func loginCookieName(state string) string { return loginCookie + state[:16] }

// SessionTTL is how long a sign-in lasts.
const SessionTTL = 12 * time.Hour

type oidcProvider struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// oidc returns the provider, doing discovery the first time. Failures
// aren't cached, so a provider that was down is retried.
func (s *Server) oidc(ctx context.Context) (*oidcProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, nil
	}
	c := s.Config.OIDC
	p, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), s.HTTPClient), c.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery at %s: %w", c.Issuer, err)
	}
	s.provider = &oidcProvider{
		oauth: oauth2.Config{
			ClientID:     c.ClientID,
			ClientSecret: s.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  s.publicURL.JoinPath("/auth/callback").String(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: c.ClientID}),
	}
	return s.provider, nil
}

// handler is a page that needs a signed-in user.
type handler func(w http.ResponseWriter, r *http.Request, u *User)

// authed loads the session, or sends the user to sign in.
func (s *Server) authed(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loginReady {
			s.notConfigured(w, r)
			return
		}
		sess, err := s.session(r)
		if err != nil {
			s.fail(w, r, nil, err)
			return
		}
		if sess == nil {
			// Only a page load goes to sign in; a subresource or fetch gets
			// 401 rather than starting a sign-in of its own.
			if dest := r.Header.Get("Sec-Fetch-Dest"); r.Method != http.MethodGet ||
				(dest != "" && dest != "document") || r.Header.Get("HX-Request") != "" {
				http.Error(w, "sign in first", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r, s.user(sess))
	})
}

func (s *Server) notConfigured(w http.ResponseWriter, r *http.Request) {
	s.error(w, r, nil, http.StatusServiceUnavailable, "Sign-in isn't configured",
		"The portal's web UI needs an identity provider: set oidc.issuer and oidc.clientId in its configuration.")
}

func (s *Server) session(r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.Store.Session(r.Context(), hashID(c.Value))
}

func hashID(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

func randomString() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// loginState travels in a short-lived cookie between /login and the
// callback.
type loginState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

// localPath accepts only a path on this site, so next can't redirect
// elsewhere.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

// cookie makes a cookie; a negative maxAge deletes it.
func (s *Server) cookie(name, value string, maxAge time.Duration) *http.Cookie {
	c := &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	return c
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginReady {
		s.notConfigured(w, r)
		return
	}
	p, err := s.oidc(r.Context())
	if err != nil {
		s.Log.Error("sign-in", "err", err)
		s.error(w, r, nil, http.StatusBadGateway, "Can't reach the identity provider",
			"Signing in needs the identity provider, which didn't answer. Try again in a minute.")
		return
	}
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(),
		Next: localPath(r.URL.Query().Get("next"))}
	b, _ := json.Marshal(st)
	http.SetCookie(w, s.cookie(loginCookieName(st.State), base64.RawURLEncoding.EncodeToString(b), 10*time.Minute))
	http.Redirect(w, r, p.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)),
		http.StatusSeeOther)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	bad := func(msg string, args ...any) {
		s.Log.Info("sign-in refused", "reason", fmt.Sprintf(msg, args...))
		s.error(w, r, nil, http.StatusBadRequest, "Sign-in failed",
			"The sign-in didn't complete. Go to the portal's home page to try again.")
	}
	if !s.loginReady {
		bad("sign-in isn't configured")
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if len(state) < 16 {
		bad("no state")
		return
	}
	var st loginState
	c, err := r.Cookie(loginCookieName(state))
	if err != nil {
		bad("no sign-in in progress with that state")
		return
	}
	http.SetCookie(w, s.cookie(c.Name, "", -1))
	if b, err := base64.RawURLEncoding.DecodeString(c.Value); err != nil || json.Unmarshal(b, &st) != nil {
		bad("malformed sign-in cookie")
		return
	}
	if e := q.Get("error"); e != "" {
		bad("identity provider: %s %s", e, q.Get("error_description"))
		return
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(st.State)) != 1 {
		bad("state mismatch")
		return
	}
	p, err := s.oidc(r.Context())
	if err != nil {
		s.fail(w, r, nil, err)
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.HTTPClient)
	tok, err := p.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		bad("code exchange: %v", err)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		bad("no id_token in the token response")
		return
	}
	idt, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		bad("ID token: %v", err)
		return
	}
	if idt.Nonce != st.Nonce {
		bad("nonce mismatch")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		bad("claims: %v", err)
		return
	}
	str := func(k string) string { v, _ := claims[k].(string); return v }
	sess := store.Session{Subject: idt.Subject, Name: str("name"), Email: str("email"),
		Groups: stringList(claims[s.Config.OIDC.GroupsClaim]), ExpiresAt: time.Now().Add(SessionTTL)}
	if sess.Name == "" {
		sess.Name = str("preferred_username")
	}
	id := randomString()
	if err := s.Store.CreateSession(r.Context(), hashID(id), sess); err != nil {
		s.fail(w, r, nil, err)
		return
	}
	s.Log.Info("signed in", "subject", sess.Subject, "groups", sess.Groups)
	http.SetCookie(w, s.cookie(sessionCookie, id, SessionTTL))
	http.Redirect(w, r, st.Next, http.StatusSeeOther)
}

// stringList reads a groups claim: a list of strings, or one string.
func stringList(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.Store.DeleteSession(r.Context(), hashID(c.Value)); err != nil && !errors.Is(err, context.Canceled) {
			s.fail(w, r, nil, err)
			return
		}
	}
	http.SetCookie(w, s.cookie(sessionCookie, "", -1))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
