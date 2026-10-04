package web

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elqsar/better-api-portal/internal/agentmcp"
	"github.com/elqsar/better-api-portal/internal/store"
)

// mcpHandler serves the MCP tools at /mcp (internal/agentmcp) over
// streamable HTTP, statelessly: the tools only read, and never call back.
func (s *Server) mcpHandler() http.Handler {
	srv := agentmcp.NewServer(mcpCatalogue{s}, s.Version)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: s.Log})
	return s.mcpAuthed(h)
}

// mcpAuthed admits requests with a live personal access token. Unlike
// authed, it takes POST, which is how MCP sends every call: all the tools
// read. A session cookie isn't enough, so no other site can make a
// signed-in browser call the tools.
func (s *Server) mcpAuthed(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loginReady {
			s.notConfigured(w, r)
			return
		}
		deny := func(msg string) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="portal"`)
			markdownError(w, http.StatusUnauthorized, "Token required", msg)
		}
		tok, ok := bearer(r)
		if !ok {
			deny("The MCP endpoint needs a personal access token, which any user can create at " +
				s.urls().Base + "/tokens: Authorization: Bearer pat_…")
			return
		}
		sess, err := s.Store.UserTokenSession(r.Context(), hashID(tok))
		if err != nil {
			s.fail(w, r, nil, err)
			return
		}
		if sess == nil {
			deny("The token is unknown, expired or revoked. Create a new one at " + s.urls().Base + "/tokens.")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// mcpCatalogue serves the tools from this portal.
type mcpCatalogue struct{ s *Server }

func (c mcpCatalogue) Index(ctx context.Context) (string, error) { return c.s.indexMD(ctx) }

func (c mcpCatalogue) Search(ctx context.Context, q agentmcp.Query) (string, error) {
	return c.s.searchMD(ctx, store.SearchQuery{Q: q.Q, Kind: q.Kind, Team: q.Team, API: q.API})
}

func (c mcpCatalogue) API(ctx context.Context, id, version string) (string, error) {
	body, _, err := c.s.apiMD(ctx, id, version)
	return body, err
}

func (c mcpCatalogue) Operation(ctx context.Context, id, version, key string) (string, error) {
	body, _, err := c.s.operationMD(ctx, id, version, key)
	return body, err
}

func (c mcpCatalogue) Event(ctx context.Context, typ string) (string, error) {
	return c.s.eventMD(ctx, typ)
}
