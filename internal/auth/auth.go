// Package auth authenticates CI callers of the REST API
// (docs/spec/05-architecture.md §Auth): OIDC ID tokens from trusted CI
// issuers, such as GitHub Actions, and the fallback static tokens an admin
// issues per repo.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"

	"better-api-portal/internal/config"
	"better-api-portal/internal/httpapi"
	"better-api-portal/internal/store"
)

// TokenPrefix starts every static token, so it can't be mistaken for an ID
// token and secret scanners can find it.
const TokenPrefix = "ptk_"

// NewToken returns a new static token and the hash to store for it.
func NewToken() (token string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b)
	token = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the stored form of a static token.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Tokens looks up static tokens; *store.Store implements it.
type Tokens interface {
	TokenByHash(ctx context.Context, hash []byte) (*store.Token, error)
}

// Headers a static-token caller may send to describe its CI run. They are
// the caller's own claims, recorded in the audit log and never checked.
const (
	HeaderRef    = "X-Portal-Ref"
	HeaderCommit = "X-Portal-Commit"
	HeaderRunURL = "X-Portal-Run-URL"
)

// CI authenticates CI jobs by the bearer token in the Authorization header.
// It implements httpapi.Authenticator.
type CI struct {
	issuers map[string]config.TrustedIssuer
	tokens  Tokens
	// client fetches the issuers' discovery documents and keys.
	client *http.Client

	mu        sync.Mutex
	verifiers map[string]*oidc.IDTokenVerifier
}

// NewCI accepts ID tokens from the given issuers and static tokens found in
// tokens. A nil client means http.DefaultClient.
func NewCI(issuers []config.TrustedIssuer, tokens Tokens, client *http.Client) *CI {
	c := &CI{issuers: map[string]config.TrustedIssuer{}, tokens: tokens, client: client,
		verifiers: map[string]*oidc.IDTokenVerifier{}}
	for _, t := range issuers {
		c.issuers[t.Issuer] = t
	}
	if c.client == nil {
		c.client = http.DefaultClient
	}
	return c
}

func unauthenticated(format string, args ...any) error {
	return fmt.Errorf("%w: %s", httpapi.ErrUnauthenticated, fmt.Sprintf(format, args...))
}

// Authenticate implements httpapi.Authenticator.
func (c *CI) Authenticate(r *http.Request) (*httpapi.Identity, error) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		return nil, unauthenticated("send a CI ID token or a portal token as Authorization: Bearer")
	}
	if strings.HasPrefix(token, TokenPrefix) {
		return c.static(r, token)
	}
	return c.idToken(r.Context(), token)
}

func (c *CI) static(r *http.Request, token string) (*httpapi.Identity, error) {
	t, err := c.tokens.TokenByHash(r.Context(), HashToken(token))
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, unauthenticated("unknown, expired or revoked portal token")
	}
	return &httpapi.Identity{
		Repo:    t.Repo,
		Actor:   fmt.Sprintf("token:%d", t.ID),
		Ref:     r.Header.Get(HeaderRef),
		Commit:  r.Header.Get(HeaderCommit),
		RunURL:  r.Header.Get(HeaderRunURL),
		CanPush: true, // a token is issued for pushing from the repo's CI
	}, nil
}

// idToken verifies a JWT from a trusted issuer and maps its claims.
func (c *CI) idToken(ctx context.Context, token string) (*httpapi.Identity, error) {
	iss, err := issuer(token)
	if err != nil {
		return nil, unauthenticated("malformed ID token: %v", err)
	}
	ti, ok := c.issuers[iss]
	if !ok {
		return nil, unauthenticated("ID tokens from %q are not trusted by this portal", iss)
	}
	v, err := c.verifier(ctx, ti)
	if err != nil {
		return nil, fmt.Errorf("issuer %s: %w", iss, err)
	}
	idt, err := v.Verify(oidc.ClientContext(ctx, c.client), token)
	if err != nil {
		return nil, unauthenticated("%v", err)
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, unauthenticated("ID token claims: %v", err)
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	repo := str(ti.RepoClaim)
	if repo == "" {
		return nil, unauthenticated("ID token has no %q claim", ti.RepoClaim)
	}
	ref := qualifiedRef(str("ref"), str("ref_type"))
	return &httpapi.Identity{
		Repo:    ti.RepoPrefix + repo,
		Actor:   idt.Subject,
		Ref:     ref,
		Commit:  str("sha"),
		RunURL:  runURL(iss, claims),
		CanPush: allowed(ti.AllowedRefs, ref),
	}, nil
}

// verifier returns the issuer's verifier, doing discovery the first time.
// Failures aren't cached, so an issuer that was down is retried.
func (c *CI) verifier(ctx context.Context, ti config.TrustedIssuer) (*oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.verifiers[ti.Issuer]; ok {
		return v, nil
	}
	// The provider keeps the context for fetching keys later, so it must
	// outlive the request.
	p, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), c.client), ti.Issuer)
	if err != nil {
		return nil, err
	}
	v := p.Verifier(&oidc.Config{ClientID: ti.Audience})
	c.verifiers[ti.Issuer] = v
	return v, nil
}

// issuer reads a JWT's iss claim without verifying it, to pick the
// verifier.
func issuer(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", err
	}
	if c.Iss == "" {
		return "", errors.New("no iss claim")
	}
	return c.Iss, nil
}

// qualifiedRef makes GitLab's short refs ("main", ref_type "branch") look
// like GitHub's ("refs/heads/main"), so allowedRefs reads the same for both.
func qualifiedRef(ref, refType string) string {
	if ref == "" || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	switch refType {
	case "branch":
		return "refs/heads/" + ref
	case "tag":
		return "refs/tags/" + ref
	}
	return ref
}

func allowed(patterns []string, ref string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, ref); ok {
			return true
		}
	}
	return false
}

// GitHubIssuer is the issuer of github.com's Actions ID tokens.
const GitHubIssuer = "https://token.actions.githubusercontent.com"

// runURL links the CI run for the audit log, where the claims allow it:
// GitHub.com (run_id) and GitLab (pipeline_id, the issuer is the instance).
func runURL(iss string, claims map[string]any) string {
	str := func(k string) string { s, _ := claims[k].(string); return s }
	switch {
	case iss == GitHubIssuer && str("repository") != "" && str("run_id") != "":
		return "https://github.com/" + str("repository") + "/actions/runs/" + str("run_id")
	case str("project_path") != "" && str("pipeline_id") != "":
		return strings.TrimSuffix(iss, "/") + "/" + str("project_path") + "/-/pipelines/" + str("pipeline_id")
	}
	return ""
}
