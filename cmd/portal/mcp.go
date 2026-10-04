package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/elqsar/better-api-portal/internal/agentmcp"
)

func mcpCmd() *cobra.Command {
	var portalURL string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve a portal's catalogue to a coding agent over MCP (stdio)",
		Long: `Run an MCP server on stdin/stdout for a coding agent such as Claude Code or
Cursor. Its tools search the portal and read APIs, operations and event types
as Markdown: search_apis, list_apis, get_api, get_operation and get_event.
They only read.

The portal is --url or $PORTAL_URL. The token is $PORTAL_TOKEN: a personal
access token (pat_…), created in the portal at /tokens.

  claude mcp add api-portal -e PORTAL_TOKEN=pat_… -- portal mcp --url https://api-portal.internal

A portal serves the same tools at /mcp, which needs no local binary:

  claude mcp add --transport http api-portal https://api-portal.internal/mcp \
    --header "Authorization: Bearer pat_…"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if portalURL == "" {
				portalURL = os.Getenv("PORTAL_URL")
			}
			if portalURL == "" {
				return errors.New("no portal: pass --url or set PORTAL_URL")
			}
			token := os.Getenv("PORTAL_TOKEN")
			if !strings.HasPrefix(token, "pat_") {
				return errors.New("set PORTAL_TOKEN to a personal access token (pat_…); create one at " +
					strings.TrimSuffix(portalURL, "/") + "/tokens")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			remote := &agentmcp.Remote{Base: strings.TrimSuffix(portalURL, "/"), Token: token,
				Client: &http.Client{Timeout: 30 * time.Second}}
			return agentmcp.NewServer(remote, portalVersion()).Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&portalURL, "url", "", "the portal's URL (default $PORTAL_URL)")
	return cmd
}
