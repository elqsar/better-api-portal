# 06 — Roadmap, open questions, decisions

## MVP

The goal is for the ~50 existing APIs to be discoverable, and for every PR that changes a spec to be checked.

Milestones are ordered so each one is usable on its own:

| # | Milestone | Done when |
|---|---|---|
| M1 | **`portal check` (offline)** — descriptor + OpenAPI + event catalogue parsing, bundling, lint, diff against `--baseline`, compat checker | Runs green on `docs/spec/examples/`; golden tests for every row of the compat table in 04-governance |
| M2 | **Registry + push** — Postgres store, `POST /push`, CI OIDC auth (GitHub or GitLab, whichever the company uses), claims, audit | Pilot service pushes from CI; rejected push shows findings in CI log |
| M3 | **Read UI** — OIDC login, API list/search, API page with Scalar docs, event pages, versions, diff view | Engineers find APIs without asking; J3–J5 journeys pass |
| M4 | **Onboarding kit** — CI templates, `portal init` (scaffold `portal.yaml`, detect specs), migration guide from existing CloudEvents+JSON Schema | 3 pilot teams onboard in < 30 min each |
| M5 | **Rollout** — Helm chart, ruleset tuning with pilot data, `warn-until` grace for new errors | ≥ 40 of 50 APIs published |

Tuning rulesets against real specs (M5) is scheduled deliberately. The OWASP measurement in [04-governance](04-governance.md#rulesets) shows how badly an untuned ruleset can misfire.

## Agent access (done, out of phase)

Brought forward from Phase 2's MCP server: `/llms.txt`, Markdown versions of every page, read-only
personal access tokens, MCP tools at `/mcp` and via `portal mcp`, and an
`AGENTS.md` section from `portal init --agents`. Still open: a diff tool
(`diff_versions`, after a Markdown diff page) and a JSON route for schemas
past the 200-line cap.

## Phase 2

| Feature | Notes |
|---|---|
| AsyncAPI v3 rendering + diff | Normalised model already exists; add templates and event-contract diff for AsyncAPI source |
| Event flow graph | Per type: producer → topic → consumers; per team: in/out |
| Try-it proxy | Server-side proxy to configured non-prod environments; forwards the user's token or a per-env service credential; allow-list of hosts; never prod by default |
| Changelog & release notes | DiffReport → Markdown; optional `CHANGELOG.md` section; RSS/Slack/Teams webhook per team |
| Security report | Auth-coverage per API (operations without security, public endpoints and their justification) |
| Deprecation workflow | Notify declared consumers' team channels on deprecation / sunset approach |

## Phase 3

| Feature | Notes |
|---|---|
| Code-first ingestion | Accept specs generated in CI (same push); later, crawl repos |
| Backstage interop | Import `catalog-info.yaml` `kind: API`; export our catalogue as Backstage entities |
| xRegistry export | Event catalogue → xRegistry message/schema registry documents |
| Kafka Schema Registry sync | Publish payload schemas to SR subjects; or import from SR to bootstrap catalogues |
| Avro / Protobuf payloads | `datacontenttype` beyond JSON |
| Tracing deep links | Link operation/message → tracing backend query (Tempo/Jaeger/Honeycomb URL templates in config). No trace ingestion |
| Per-environment binding overrides | Topic names that differ per env |

## Open questions

| # | Question | Default until answered |
|---|---|---|
| Q1 | GitHub or GitLab (or both) for CI? | Build GitHub Actions OIDC first |
| Q2 | Do any APIs need restricted visibility (e.g. security, HR)? | No — all visible to all employees |
| Q3 | Do topic/subject names differ between environments? | Assume identical; revisit (P3 item) if not |
| Q4 | Is there an existing Kafka Schema Registry that is the source of truth for some schemas? | Portal is source of truth for contracts; SR sync later |
| Q5 | Who owns org rulesets — platform team or an API guild? | Platform team, changes via PR to config repo |
| Q6 | Company event type prefix (`com.acme.`)? Existing types that violate `ce-type-format`? | Inventory existing types in M4; grandfather via ack list |
| Q7 | Do existing JSON Schemas use keywords outside the compat checker subset (`oneOf` etc.)? | Measure in M1 against real schemas; each such change needs major bump or ack |
| Q8 | Open-source licence and name | Apache-2.0; name TBD |

## Decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | API-only portal, not a general software catalogue | Stay small; interoperate with Backstage instead of competing |
| D2 | Design-first; specs live in service repos and are pushed from CI | Matches how teams work; no crawler to maintain |
| D3 | Own event catalogue format over requiring AsyncAPI | Teams already have CloudEvents + JSON Schema; ~20 lines to adopt vs. learning AsyncAPI; xRegistry-exportable for interop |
| D4 | AsyncAPI v3 accepted, v2 rejected | Avoid supporting two AsyncAPI generations; v3 is current |
| D5 | Version lives in the spec, not the descriptor | Single source of truth |
| D6 | Compat default per message role (produces→FORWARD, receives→BACKWARD) | Protects whichever side doesn't control the upgrade |
| D7 | Unverifiable schema changes are treated as breaking | Missed breaking events are the costliest failure; acks give an escape hatch |
| D8 | Curated OWASP subset, not full hard mode | Full mode scored a reasonable spec 10/100; noise kills linter adoption |
| D9 | Rulesets are central (portal config), not per repo | Consistent standards; teams can pick only admin-approved rulesets |
| D10 | Single Go binary + Postgres only; server-rendered UI with htmx | Low ops cost, easy contributions, credible OSS story |
| D11 | Tracing out of scope beyond deep links | Runtime data is a different product |
| D12 | CI auth via OIDC federation; static tokens only as fallback | No long-lived secrets in CI |
| D13 | Agents read Markdown (`/llms.txt`, `.md` pages, MCP tools returning the same pages), not raw specs | Schemas inlined as field lists need no `$ref` resolution and cost fewer tokens; one renderer serves every agent surface |
| D14 | Agents authenticate with read-only personal access tokens (`pat_`), separate from CI tokens | Agents can't do a browser OIDC sign-in; a token that only reads, as its user, can't publish if it leaks |
