// Package agentmcp is the portal's MCP server: read-only tools that let a
// coding agent search the catalogue and read an API, operation or event
// type as the Markdown of internal/agentdoc.
//
// The tools read through a Catalogue: in the portal itself (portal serve's
// /mcp), or over HTTP from a portal's Markdown pages (portal mcp, stdio).
package agentmcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elqsar/better-api-portal/internal/agentdoc"
)

// Catalogue renders the catalogue's pages as Markdown. An error's message
// is shown to the agent, so it should say what to do next.
type Catalogue interface {
	Index(ctx context.Context) (string, error)
	Search(ctx context.Context, q Query) (string, error)
	// API and Operation take "" for the latest version.
	API(ctx context.Context, id, version string) (string, error)
	Operation(ctx context.Context, id, version, key string) (string, error)
	Event(ctx context.Context, typ string) (string, error)
}

// Query is a search.
type Query struct {
	Q    string `json:"query" jsonschema:"words to look for, web-search style: a quoted phrase, -word to exclude"`
	Kind string `json:"kind,omitempty" jsonschema:"only this kind of result: operation, message (an event type), schema or api"`
	Team string `json:"team,omitempty" jsonschema:"only APIs owned by this team slug"`
	API  string `json:"api_id,omitempty" jsonschema:"only results in this API"`
}

type apiArgs struct {
	ID      string `json:"api_id" jsonschema:"the API's id, e.g. orders-http"`
	Version string `json:"version,omitempty" jsonschema:"a published version such as 2.3.0; the latest if omitted"`
}

type operationArgs struct {
	ID        string `json:"api_id" jsonschema:"the API's id, e.g. orders-http"`
	Operation string `json:"operation" jsonschema:"the operationId, or the method and path, e.g. GET /orders/{orderId}"`
	Version   string `json:"version,omitempty" jsonschema:"a published version such as 2.3.0; the latest if omitted"`
}

type eventArgs struct {
	Type string `json:"type" jsonschema:"the CloudEvents type, e.g. com.acme.orders.order.created.v1"`
}

const instructions = `The company's API portal: every HTTP API (OpenAPI) and event type (CloudEvents), with its owner, contract and consumers.
Look contracts up here instead of guessing endpoints, fields or payloads. Start with search_apis, or list_apis for the whole index;
get_api lists an API's operations or event types; get_operation and get_event give the full request, response or payload schema.`

// NewServer returns the MCP server over c.
func NewServer(c Catalogue, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "api-portal", Title: "API portal", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}

	mcp.AddTool(s, &mcp.Tool{Name: "search_apis", Annotations: readOnly,
		Description: "Search the API portal: APIs, HTTP operations, event types and schemas. Each result links to its page; " +
			"read operations with get_operation, event types with get_event."},
		func(ctx context.Context, _ *mcp.CallToolRequest, q Query) (*mcp.CallToolResult, any, error) {
			return text(c.Search(ctx, q))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_apis", Annotations: readOnly,
		Description: "List every API in the portal by owning team, with its kind, latest version and a summary."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return text(c.Index(ctx))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_api", Annotations: readOnly,
		Description: "Get an API: what it is, its owner, environments, its operations or event types, and who consumes it."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a apiArgs) (*mcp.CallToolResult, any, error) {
			return text(c.API(ctx, a.ID, a.Version))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_operation", Annotations: readOnly,
		Description: "Get an HTTP operation: parameters, request body and responses with their schemas, and the auth it needs."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a operationArgs) (*mcp.CallToolResult, any, error) {
			return text(c.Operation(ctx, a.ID, a.Version, OperationKey(a.Operation)))
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_event", Annotations: readOnly,
		Description: "Get an event type: its CloudEvents attributes, topics, payload schema, examples, producer and consumers."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a eventArgs) (*mcp.CallToolResult, any, error) {
			return text(c.Event(ctx, a.Type))
		})
	return s
}

// OperationKey turns what an agent passes for an operation, an
// operationId or "METHOD /path", into the key the pages use.
func OperationKey(op string) string {
	op = strings.TrimSpace(op)
	if method, path, ok := strings.Cut(op, " "); ok && strings.HasPrefix(strings.TrimSpace(path), "/") {
		return agentdoc.OperationKey(method, strings.TrimSpace(path), "")
	}
	return op
}

// text makes a tool result of a page. An error becomes a tool error, which
// the agent reads, rather than a protocol error.
func text(page string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: page}}}, nil, nil
}
