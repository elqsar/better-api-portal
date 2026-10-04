package agentmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxPage caps a page read from the portal.
const maxPage = 4 << 20

// Remote reads a portal's Markdown pages over HTTP with a personal access
// token: the Catalogue of portal mcp.
type Remote struct {
	Base   string // the portal's URL, without a trailing slash
	Token  string // a personal access token, pat_…
	Client *http.Client
}

func (r *Remote) Index(ctx context.Context) (string, error) {
	return r.get(ctx, "/llms.txt")
}

func (r *Remote) Search(ctx context.Context, q Query) (string, error) {
	v := url.Values{"q": {q.Q}}
	for k, x := range map[string]string{"kind": q.Kind, "team": q.Team, "api": q.API} {
		if x != "" {
			v.Set(k, x)
		}
	}
	return r.get(ctx, "/search.md?"+v.Encode())
}

func (r *Remote) API(ctx context.Context, id, version string) (string, error) {
	if version == "" {
		return r.get(ctx, "/apis/"+url.PathEscape(id)+".md")
	}
	return r.get(ctx, "/apis/"+url.PathEscape(id)+"/versions/"+url.PathEscape(version)+".md")
}

func (r *Remote) Operation(ctx context.Context, id, version, key string) (string, error) {
	if version == "" {
		version = "latest"
	}
	return r.get(ctx, "/apis/"+url.PathEscape(id)+"/versions/"+url.PathEscape(version)+
		"/operations/"+url.PathEscape(key)+".md")
}

func (r *Remote) Event(ctx context.Context, typ string) (string, error) {
	return r.get(ctx, "/events/"+url.PathEscape(typ)+".md")
}

// get fetches a page. A page the portal refuses comes back as an error
// with the portal's explanation, which is Markdown meant for the agent.
func (r *Remote) get(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Base+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Accept", "text/markdown")
	c := r.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("the portal can't be reached: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(string(b))
	if resp.StatusCode != http.StatusOK {
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/markdown") || body == "" {
			body = fmt.Sprintf("the portal answered %s", resp.Status)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			body += "\n\n(portal mcp reads the token from $PORTAL_TOKEN.)"
		}
		return "", errors.New(body)
	}
	return string(b), nil
}
