// Package client calls a portal's REST API from CI: portal push, and check
// --baseline-from.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"better-api-portal/internal/auth"
	"better-api-portal/internal/bundle"
	"better-api-portal/internal/check"
	"better-api-portal/internal/httpapi"
)

// Client talks to one portal.
type Client struct {
	// BaseURL is the portal's URL, e.g. https://api-portal.internal.
	BaseURL string
	// Credentials authenticate every request.
	Credentials *Credentials
	HTTP        *http.Client
	// Backoff is the wait before each retry; nil means 1s, 2s, 4s.
	Backoff []time.Duration
}

// New returns a client for the portal at baseURL.
func New(baseURL string, creds *Credentials) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("portal URL %q is not an http(s) URL", baseURL)
	}
	return &Client{BaseURL: strings.TrimSuffix(baseURL, "/"), Credentials: creds,
		HTTP: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// Error is a response the portal refused with; retrying won't help.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized:
		hint = " (in GitHub Actions, give the job permissions: id-token: write; elsewhere set PORTAL_TOKEN)"
	}
	return fmt.Sprintf("portal: %d %s: %s%s", e.Status, http.StatusText(e.Status), e.Message, hint)
}

// Push sends a descriptor and its bundles to /api/v1/push, or /check with
// dryRun. A push is idempotent by content hash, so network errors and 5xx
// responses are retried.
func (c *Client) Push(ctx context.Context, descriptor []byte, bundles map[string]*bundle.Bundle,
	acks map[string]string, dryRun bool) (*httpapi.PushResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(httpapi.PartDescriptor, check.DescriptorName)
	if err != nil {
		return nil, err
	}
	fw.Write(descriptor)
	for _, id := range slices.Sorted(maps.Keys(bundles)) {
		fw, err := mw.CreateFormFile(httpapi.PartBundlePrefix+id, id+".tar.zst")
		if err != nil {
			return nil, err
		}
		if err := bundles[id].Pack(fw); err != nil {
			return nil, fmt.Errorf("bundle %s: %w", id, err)
		}
	}
	if len(acks) > 0 {
		fw, err := mw.CreateFormField(httpapi.PartAcks)
		if err != nil {
			return nil, err
		}
		if err := json.NewEncoder(fw).Encode(acks); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	path := "/api/v1/push"
	if dryRun {
		path = "/api/v1/check"
	}
	resp, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body.Bytes()))
		if err == nil {
			req.Header.Set("Content-Type", mw.FormDataContentType())
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out httpapi.PushResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("portal: reading the push response: %w", err)
	}
	return &out, nil
}

// Latest returns the latest published version of an API as a baseline, and
// its version; nil if the API has none.
func (c *Client) Latest(ctx context.Context, apiID string) (*check.Baseline, string, error) {
	resp, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet,
			c.BaseURL+"/api/v1/apis/"+url.PathEscape(apiID)+"/versions/latest/bundle", nil)
	})
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := bundle.Unpack(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("portal: bundle of %s: %w", apiID, err)
	}
	if want := resp.Header.Get(httpapi.HeaderContentHash); want != "" {
		if got, err := b.Hash(); err != nil || got != want {
			return nil, "", fmt.Errorf("portal: bundle of %s has hash %s, but the portal says %s", apiID, got, want)
		}
	}
	return &check.Baseline{Bundle: b, Lifecycle: resp.Header.Get(httpapi.HeaderLifecycle)},
		resp.Header.Get(httpapi.HeaderVersion), nil
}

// do sends a request, built afresh for each attempt, with credentials, and
// returns a 2xx response. Other responses are *Error; network errors and 5xx
// are retried.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	backoff := c.Backoff
	if backoff == nil {
		backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	}
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		if err := c.Credentials.apply(ctx, req); err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err == nil && resp.StatusCode < 300 {
			return resp, nil
		}
		if err == nil {
			err = responseError(resp)
			if resp.StatusCode < 500 {
				return nil, err
			}
		}
		if attempt >= len(backoff) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-time.After(backoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func responseError(resp *http.Response) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e httpapi.ErrorResponse
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	return &Error{Status: resp.StatusCode, Message: msg}
}

// Credentials say how to authenticate, found in the CI environment by
// FromEnv.
type Credentials struct {
	// Token is a static portal token or an ID token, sent as is.
	Token string
	// github requests a fresh ID token from GitHub Actions for audience.
	githubURL, githubToken, audience string
	// Run describes the CI run to the portal, for static tokens.
	Run map[string]string
}

// FromEnv finds credentials in the environment: PORTAL_TOKEN (a portal
// token, or an ID token, as GitLab's id_tokens provides), else GitHub
// Actions' ID token request variables. It returns nil if there are none.
func FromEnv(getenv func(string) string, audience string) *Credentials {
	c := &Credentials{Run: runHeaders(getenv)}
	switch {
	case getenv("PORTAL_TOKEN") != "":
		c.Token = getenv("PORTAL_TOKEN")
	case getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "":
		c.githubURL = getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
		c.githubToken = getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
		c.audience = audience
	default:
		return nil
	}
	return c
}

// ErrNoCredentials means the environment has nothing to authenticate with.
var ErrNoCredentials = errors.New("no credentials: set PORTAL_TOKEN, or in GitHub Actions give the job permissions: id-token: write")

func (c *Credentials) apply(ctx context.Context, req *http.Request) error {
	if c == nil {
		return ErrNoCredentials
	}
	if c.Token == "" && c.githubURL != "" {
		t, err := githubIDToken(ctx, c.githubURL, c.githubToken, c.audience)
		if err != nil {
			return err
		}
		c.Token = t // valid for minutes; enough for one command
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if strings.HasPrefix(c.Token, auth.TokenPrefix) {
		for k, v := range c.Run {
			req.Header.Set(k, v)
		}
	}
	return nil
}

// githubIDToken asks the Actions runtime for an ID token.
func githubIDToken(ctx context.Context, requestURL, requestToken, audience string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL: %w", err)
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting a GitHub Actions ID token: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Value string `json:"value"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("requesting a GitHub Actions ID token: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Value == "" {
		return "", fmt.Errorf("requesting a GitHub Actions ID token: no token in the response")
	}
	return out.Value, nil
}

// runHeaders describe the CI run from GitHub Actions' or GitLab CI's
// variables. The portal records them for static tokens only; an ID token
// carries its own.
func runHeaders(getenv func(string) string) map[string]string {
	h := map[string]string{}
	set := func(k, v string) {
		if v != "" {
			h[k] = v
		}
	}
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		set(auth.HeaderRef, getenv("GITHUB_REF"))
		set(auth.HeaderCommit, getenv("GITHUB_SHA"))
		if s, r, id := getenv("GITHUB_SERVER_URL"), getenv("GITHUB_REPOSITORY"), getenv("GITHUB_RUN_ID"); s != "" && r != "" && id != "" {
			set(auth.HeaderRunURL, s+"/"+r+"/actions/runs/"+id)
		}
	case getenv("GITLAB_CI") == "true":
		if t := getenv("CI_COMMIT_TAG"); t != "" {
			set(auth.HeaderRef, "refs/tags/"+t)
		} else if b := getenv("CI_COMMIT_BRANCH"); b != "" {
			set(auth.HeaderRef, "refs/heads/"+b)
		}
		set(auth.HeaderCommit, getenv("CI_COMMIT_SHA"))
		set(auth.HeaderRunURL, getenv("CI_PIPELINE_URL"))
	}
	return h
}
