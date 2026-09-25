# 01 — Product

## Feature map

Every item from the original `idea.md`, and where it lands.

| Idea feature | What it means here | Phase |
|---|---|---|
| API Catalogue (OpenAPI, AsyncAPI) | Registry + browse/search UI for OpenAPI, CloudEvents catalogues; AsyncAPI v3 ingested | MVP (AsyncAPI rendering: P2) |
| API Explorer | Rendered reference docs, schema browser, examples | MVP |
| — "try it" | Authenticated request proxy to internal environments | P2 |
| Developer tools — CLI, CI/CD | `portal check` / `push` / `diff`; GitHub Actions + GitLab CI templates | MVP |
| Version diff | Semantic diff between any two versions, breaking changes highlighted | MVP |
| API Linter | Rulesets per spec kind, quality score per version | MVP |
| API Security Audit | OWASP API Top 10 ruleset + auth-coverage report | MVP (ruleset), P2 (report) |
| Versioning / release management | Semver enforcement, lifecycle states, changelog | MVP (semver, lifecycle), P2 (changelog/release notes) |
| Tracing | Deep links from an operation/event to the tracing backend | P3 |

Additions not in the original idea:

| Feature | Why | Phase |
|---|---|---|
| Event flow graph | Who produces / consumes each event type — the main question for async | P2 |
| MCP server | Lets coding agents query the catalogue | P2 |
| Backstage / xRegistry interop | Easier adoption and open-sourcing | P3 |

## User journeys (MVP)

Each MVP feature must be reachable through at least one of these.

### J1 — Onboard a service
1. Owner adds `portal.yaml` next to their `openapi.yaml` / `events.yaml`.
2. Owner adds the CI template (one job: `portal check` on PRs, `portal push` on
   the main branch or on tags).
3. First push registers the API(s) under the owner's team. The portal URL is
   printed in the CI log.

**Acceptance:** from zero to visible in the catalogue with ≤ 2 files changed and
no manual step in the portal UI.

### J2 — Catch a breaking change in a PR
1. Developer removes a response field in `openapi.yaml` without bumping major.
2. `portal check` fetches the latest published version, diffs, and fails with a
   list of breaking changes and the rule that requires a major bump.
3. Developer either reverts or bumps `version` to the next major; check passes.

**Acceptance:** check runs offline apart from one fetch of the previous
version; result in < 10 s for a 5 000-line spec; output is readable in CI logs
and also emitted as SARIF / JUnit for PR annotations.

### J3 — Find an API
1. Consumer searches "refund".
2. Results include HTTP operations (`POST /orders/{id}/refunds`), event types
   (`com.acme.orders.refund.issued.v1`) and schemas mentioning the word, grouped
   by API with owner and lifecycle.
3. Consumer opens the API page: rendered docs, versions, owner contact, lint
   score, environments.

**Acceptance:** search across all APIs returns in < 300 ms at 500 APIs.

### J4 — Understand an event
1. Consumer opens an event type.
2. Sees: description, CloudEvents attributes (`type`, `source` pattern,
   `datacontenttype`), the payload JSON Schema rendered as a tree plus an
   example, and bindings (e.g. Kafka topic `orders.events`, key `orderId`,
   binary mode).
3. Sees which APIs declare themselves producer or consumer of this type.

### J5 — Compare versions
1. From an API's version list, pick two versions.
2. See a structured diff (added / removed / changed operations, messages,
   schema fields), breaking items flagged, plus a raw text diff tab.

### J6 — Deprecate an API
1. Owner sets `lifecycle: deprecated` and optionally `sunset: 2027-03-31` in
   `portal.yaml`, pushes.
2. Catalogue shows a deprecation banner; the API is de-ranked in search; a
   report lists deprecated APIs and their declared consumers.

### J7 — Platform team sets standards
1. Admin edits the org ruleset (a YAML file in a config repo, or mounted into
   the deployment).
2. Next push of every API is evaluated against it; the dashboard shows lint
   scores per team.

**Rule:** changing a ruleset never retroactively breaks already-published
versions — scores are recomputed, but gating applies only to new pushes.

## Non-functional requirements

| Area | Requirement |
|---|---|
| Availability | Reads are the priority; CI push may fail during a deploy and must be retry-safe (idempotent by content hash). |
| Latency | Page loads < 1 s p95; search < 300 ms p95. |
| Security | All UI behind OIDC. CI auth via short-lived OIDC tokens. No spec content leaves the cluster (no SaaS dependencies at runtime). |
| Auditability | Every push and admin change recorded with actor, time and source (repo, commit SHA, CI run URL). |
| Portability | Postgres is the only required dependency. Runs on any Kubernetes; also `portal serve` locally with a dev database. |
| Open-source readiness | No company names in code; company-specific values come from config. Apache-2.0-compatible dependencies only. |
