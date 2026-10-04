package initkit

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The agents section sits between these markers, so a rerun replaces it
// and leaves the rest of the file alone.
const (
	agentsStart = "<!-- api-portal:start (written by portal init --agents; rerun it to update) -->"
	agentsEnd   = "<!-- api-portal:end -->"
)

// AgentsFile is the instructions file the section goes into: AGENTS.md,
// or CLAUDE.md if only that one exists.
func AgentsFile(root string) string {
	agents := filepath.Join(root, "AGENTS.md")
	if _, err := os.Stat(agents); err != nil {
		if claude := filepath.Join(root, "CLAUDE.md"); exists(claude) {
			return claude
		}
	}
	return agents
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// AgentsSection tells a coding agent working in the repo about the
// portal: look contracts up there, how to reach it, and to check spec
// changes before committing them.
func AgentsSection(portalURL string, apis []API) []byte {
	base := strings.TrimSuffix(portalURL, "/")
	var b bytes.Buffer
	fmt.Fprintln(&b, agentsStart)
	fmt.Fprintln(&b, "## API portal")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "The contracts of every HTTP API and event type in the company are in the API portal, %s.\n", base)
	fmt.Fprintln(&b, "Before writing code against another service's endpoint or event, look its contract up there;")
	fmt.Fprintln(&b, "don't guess paths, fields or payloads.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "- MCP tools (`search_apis`, `get_api`, `get_operation`, `get_event`). Add them once, with a personal")
	fmt.Fprintf(&b, "  access token from %s/tokens:\n", base)
	fmt.Fprintf(&b, "  `claude mcp add --transport http api-portal %s/mcp --header \"Authorization: Bearer $PORTAL_TOKEN\"`\n", base)
	fmt.Fprintln(&b, "- Without MCP, every page is Markdown with the same token:")
	fmt.Fprintf(&b, "  `curl -H \"Authorization: Bearer $PORTAL_TOKEN\" '%s/search.md?q=refund'`.\n", base)
	fmt.Fprintf(&b, "  The index is %s/llms.txt; an API is %s/apis/<id>.md; an event type is %s/events/<type>.md.\n", base, base, base)
	if len(apis) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "This repo publishes these APIs from `portal.yaml`:")
		fmt.Fprintln(&b)
		for _, a := range apis {
			fmt.Fprintf(&b, "- `%s`: `%s`\n", a.ID, a.Spec.Path)
		}
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "After changing one of these specs, run `portal check --baseline-from "+base+"`: it lints the")
		fmt.Fprintln(&b, "spec and fails on a breaking change that the version doesn't allow.")
	}
	fmt.Fprintln(&b, agentsEnd)
	return b.Bytes()
}

// MergeAgents puts section into an instructions file's content: in place
// of an earlier section, or at the end.
func MergeAgents(existing, section []byte) []byte {
	start := bytes.Index(existing, []byte(agentsStart))
	if start >= 0 {
		if end := bytes.Index(existing[start:], []byte(agentsEnd)); end >= 0 {
			end += start + len(agentsEnd)
			if end < len(existing) && existing[end] == '\n' {
				end++
			}
			out := append([]byte{}, existing[:start]...)
			out = append(out, section...)
			return append(out, existing[end:]...)
		}
	}
	out := bytes.TrimRight(existing, "\n")
	if len(out) > 0 {
		out = append(append([]byte{}, out...), "\n\n"...)
	}
	return append(out, section...)
}
