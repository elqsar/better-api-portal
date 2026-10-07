# Agent access

Coding agents can use the catalogue: agentdoc Markdown, `/llms.txt` and `.md`
pages, personal access tokens, MCP tools (`/mcp`, `portal mcp`), and
`portal init --agents`. Recorded as D13 and D14 in
[06-roadmap](../spec/06-roadmap.md). Open: a
`diff_versions` tool and a JSON route for schemas past the 200-line cap.

- **Markdown is the agent format**, not raw specs. Schemas are inlined as
  field lists (`- \`amount\` (integer, required, Money): Minor units.
  format: int64.`), which take fewer tokens than JSON and need no `$ref`
  resolution. One schema stops at 200 lines with a count of the rest.
- **One page per operation and event type**, so an agent reads only what it
  needs. An API page is an index of them. `full` concatenates them, with
  headings shifted down, for llms-full.txt (per team, because the whole
  catalogue won't fit in a context).
- Operations without an `operationId` are keyed `get-orders-orderId`
  (`OperationKey`).
- Responses that differ only by status are merged (`401, 404, 409: Error…`).
- **OpenAPI schemas resolve through the entry file's whole document**
  (`agentdoc.Load`), not `spec.Schemas`: component schemas are keyed by
  pointer there, so their `#/components/...` refs didn't resolve, and inline
  request/response schemas weren't in it at all. Parameter and response
  descriptions and security schemes also come from the raw document,
  because the model doesn't carry them.
- agentdoc doesn't import the store: callers fill `agentdoc.API`/`Entry`.
- Retired APIs are left out of llms.txt, as search hides them.
- **Routes:** Go's mux can't match `{id}.md`, so `.md` is stripped from the
  last path value in the existing handlers (`trimMD`), which dispatch on
  `wantsMarkdown`: a `.md`/`llms*.txt` path, or an `Accept` that lists
  `text/markdown` before `text/html` (browsers list HTML first). Only
  operations have a new route; they have no HTML page.
- Operation pages answer to the operationId and to the method-and-path key,
  because search hits ("GET /orders/{orderId}") don't carry the operationId.
- A pinned version's page gets an ETag (content hash + `agentDocVersion`);
  latest, event, search and llms pages don't, since their content moves
  with pushes.
- `llms-full.txt` stops adding APIs after 512 KiB and lists the rest with
  links. llms.txt summarises each API's description: its first paragraph,
  at most 200 characters (`store.APISummary.Description`, new).
- A request for Markdown without a session gets a Markdown 401, not a
  sign-in redirect; `s.error` answers Markdown requests in Markdown.
- Not done: a `schemas/{ptr}.json` route for schemas past the 200-line cap.
- **Personal access tokens** (`pat_` + 32 random bytes, sha256 stored) live
  in `user_tokens`, not `ci_tokens`: CI tokens publish for a repo, PATs
  read as a user. A PAT stands in for the session in `authed`, on GET and
  HEAD only; other methods get 403, and so does `/tokens` itself (a token
  can't mint tokens). A bearer that isn't a live PAT is a 401: no fallback
  to the cookie.
- A token snapshots the user's name, email and groups when it's made, like
  a session does at sign-in. A user who leaves a team keeps that team's
  read view (rejected pushes) through the token until it expires or is
  revoked; reads are open to everyone anyway (Q2).
- Expiry is 90 (default), 30 or 365 days; at most 20 live tokens per user.
  Creating and revoking are audited (`user-token.create`, `.revoke`).
- The token is shown once, in the POST's response (`Cache-Control:
  no-store`), with a `curl` line to try it.
- llms.txt is headed with the org's name, like the web pages.
- **MCP tools return the same Markdown as the pages**, through one
  `agentmcp.Catalogue` interface. The web package renders each page with an
  `*MD` method returning `(body, error)`; a `docError` carries a status and
  a message the agent can act on, and becomes a tool error (`IsError`), not
  a protocol error.
- Five tools, not the roadmap's list: `list_dependencies` is part of
  `get_api` (Consumers/Consumes), `get_schema` isn't needed while schemas
  are inlined, and `diff_versions` waits for a Markdown diff page.
- `/mcp` is stateless streamable HTTP with JSON responses: the tools only
  read and never call back. It takes **POST with a PAT only**: a session
  cookie isn't accepted, so another site can't make a signed-in browser
  call it, and `sameOrigin` still refuses a foreign Origin.
- `portal mcp` needs no database: `agentmcp.Remote` GETs the `.md` pages
  with `$PORTAL_TOKEN`, and passes the portal's Markdown error on as the
  tool error. The integration test checks it gives the same text as `/mcp`.
- Operation pages also answer at `/versions/latest/operations/{op}.md`.
- `portal init --agents` writes between `<!-- api-portal:start … -->` and
  `<!-- api-portal:end -->`, so a rerun replaces only its section, and it
  isn't subject to `--force`. It writes to AGENTS.md, or to CLAUDE.md when
  only that exists. The section lists the repo's API ids and spec paths, and
  says to run `portal check --baseline-from` after changing a spec.
