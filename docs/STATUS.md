# Implementation status

Last updated: 2026-10-07

A handoff note for continuing the work: where we are, what each commit did,
the code map, next steps and known gaps. The spec is in [spec/](spec/README.md),
the roadmap and milestones in [spec/06-roadmap.md](spec/06-roadmap.md), and
the decisions taken along the way in [decisions/](decisions/README.md).

## Where we are

| Milestone | State | Still open |
|---|---|---|
| **M1** `portal check` (offline) | Done | — |
| **M2** registry + push | Code-complete | A pilot service pushing from CI |
| **M3** read UI | Code-complete; J3–J5 acceptance tests pass | Engineers find APIs without asking |
| **M4** onboarding kit | Code-complete; J1 acceptance test passes | 3 pilot teams onboard in under 30 minutes each |
| **M5** rollout | Not started | Helm chart, rulesets from files, `warn-until`, tuning |

Every open "done when" for M2–M4 is operational and waits on the same thing:
a deployed portal (see "Next steps"). Agent access (rows 35–40) was brought
forward from Phase 2 and is done.

- **M1:** event catalogues and OpenAPI both get the full pipeline: parse,
  lint, diff against `--baseline`, then the semver gate. Specs are bundled
  with a content hash. Reports come as text, JSON, SARIF and JUnit.
- **M2:** store, push API, CI auth, `portal push`, `check --baseline-from`,
  an example workflow, and an end-to-end test of the CLI against a real
  portal.
- **M3:** all eight steps (see [decisions/web-ui.md](decisions/web-ui.md)).
  Tuning against real specs moves to M5, as the roadmap schedules it,
  because no real specs are available yet.
- **M4:** `portal init` (with `--ci github`, `--events-from` and `--agents`)
  and the onboarding guide (see [decisions/onboarding.md](decisions/onboarding.md)).

## Commits

| # | Commit | What |
|---|---|---|
| 0 | `a54337b` | Specification draft v0.1 |
| 1 | `ac4806e` | `portal check` + `portal.yaml` validation (embedded JSON Schema, line numbers) |
| 2 | `32b82e2` | Event catalogue → normalised model (defaults merge, sandboxed `$ref` loading) |
| 3 | `1c14a7b` | Native CloudEvents lint rules, score, `--config` |
| 4 | `e4e1e3c` | JSON Schema compatibility checker (`internal/compat`), every row of the governance table tested |
| 5 | `952c256` | Event contract diff, `--baseline`, `portal diff`, semver gate, `--ack` |
| 6 | `7800aca` | OpenAPI → model, `openapi-default` lint (vacuum recommended + org + security rules) |
| 7 | `efe5c77` | vacuum decision: keep it, drop `oas3-missing-example` and `component-description` |
| 8 | `c882e39` | OpenAPI diff with oasdiff: `--baseline`, `portal diff`, `BRK-OA-` ids |
| 9 | `1b600e3` | Bundles: content hash, deterministic tar.zst, `portal bundle`, `--baseline id=bundle` |
| 10 | `4c5d84b` | SARIF 2.1.0 and JUnit reports: `--format sarif\|junit`, repeatable `--output format=path` |
| 11 | `9d224aa` | Postgres store (pgx, embedded goose migrations), `portal migrate` |
| 12 | `515dd72` | `check.RunBundles`: check an uploaded descriptor + bundles in memory |
| 13 | `e7073ff` | `portal serve`: push, check (dry run), bundle download |
| 14 | `b703255` | CI auth: GitHub/GitLab OIDC ID tokens, static tokens, `portal admin token` |
| 15 | `fe8f1ec` | `portal push`, `check --baseline-from`, `internal/client`, example GitHub Actions workflow |
| 16 | `109eb2d` | End-to-end test: the CLI against a real portal (publish, retry, reject, ack) |
| 17 | `62b0a3c` | M3 index: `version_models`, message/operation/binding rows, `dependencies`, `search_docs`, `latest_version_id`; `portal reindex` |
| 18 | `06eb74f` | Web UI skeleton: layout, embedded static files, OIDC sign-in with PKCE, Postgres sessions, `serve --dev-login` stub provider |
| 19 | `8a5833f` | API list with filters, API pages (overview, lint & changes, versions), deprecation banner |
| 20 | `6f180f2` | Docs tab: Scalar (vendored, offline) over a server-resolved single OpenAPI document |
| 21 | `7ebca18` | Event page `/events/{type}`: CE attributes, payload schema tree, examples, bindings with broker links, owner and consumers; `brokers` config, `descriptor-broker-unknown` |
| 22 | `c09b395` | Search `/search`: full text + title trigrams, grouped by API, deprecated ranked lower, retired hidden; top-bar box; 500-API latency test, `task bench:search` |
| 23 | `bb50c06` | Diff page `/apis/{id}/diff`: contract changes between any two versions (`check.DiffBundles`), raw per-file diff as pushed or canonical JSON, folds expanded by htmx; compare form on the versions tab |
| 24 | `4659940` | J3–J5 acceptance tests (`TestJourney*`) with golden HTML of each step's `<main>`, `payments-service` fixture, `task test:journeys`; removed event types aren't linked |
| 25 | `deb7693` | Server rules `ce-type-unique` (error) and `ce-topic-single-owner` (warn) in push and `--dry-run`, against the portal and the rest of the push; they count in the score |
| 26 | `5b39805` | CLI distribution: module `github.com/elqsar/better-api-portal`, `portal version`, `task release` (static tar.gz + SHA256SUMS), `Containerfile` + `task image`; the example workflow installs a pinned release |
| 27 | `c76fb54` | An `unchanged` push updates the API's metadata (descriptor-only changes need no version bump) |
| 28 | `88cd609` | Consumers grouped by service (repo) on the event page and the dependencies tab |
| 29 | `90ac5ba` | Compatible payload changes name their fields (`compat.FieldChanges`) |
| 30 | `a439b02` | API list and page titles fall back to the latest spec's title |
| 31 | `ac98731` | M4: `portal init` (detect specs, propose ids, write `portal.yaml`, check it) and `--ci github` from the embedded workflow template |
| 32 | `ade16db` | M4: `portal init --events-from`: draft an event catalogue from existing JSON Schemas, with a type inventory (Q6) |
| 33 | `397af16` | M4: onboarding guide `docs/guide/onboarding.md` |
| 34 | `3c8c7c4` | M4: J1 acceptance test (`TestJourneyOnboardAService`): `initkit` on the payments fixture adds 2 files, the first push lists both APIs |
| 35 | `45df3a8` | Agents, step 1: `internal/schematree` (the payload tree, moved out of `web`), `internal/agentdoc` (model → Markdown: llms.txt, API, operation and event pages; golden tests) |
| 36 | `66be7f5` | Agents, step 2: `/llms.txt`, `/llms-full.txt?team=\|tag=\|kind=`, `/search.md`, `.md` (or `Accept: text/markdown`) on API, version, operation and event pages; Markdown errors and 401 instead of a sign-in redirect |
| 37 | `f53d1ba` | Agents, step 3: read-only personal access tokens (`pat_`): `user_tokens` (migration 00005), `/tokens` page to create, list and revoke; `Authorization: Bearer pat_…` on any GET |
| 38 | `77a8644` | Agents, step 4: MCP server (`internal/agentmcp`, go-sdk v1.8.0): `search_apis`, `list_apis`, `get_api`, `get_operation`, `get_event`; `/mcp` in `portal serve` (PAT), `portal mcp` (stdio, reads the `.md` pages) |
| 39 | `80239fc` | Agents, step 5: `portal init --agents` (an API portal section in AGENTS.md or CLAUDE.md, replaced on rerun); guide section "Using the portal from AI agents"; roadmap D13, D14 |
| 40 | `31da554` | "Ask AI" menu on API, operation (overview table) and event pages: open in ChatGPT or Claude with a prompt linking the `.md` page and `/llms.txt`, copy the prompt, copy the Markdown, view as Markdown (`_ask_ai.html`, `static/ai.js`) |
| 41 | `b6f96b2` | A descriptor-only change rewrites the API's search document (`index.APIDoc`), so search shows the new title and tags |
| 42 | `f309f19` | Apache-2.0 `LICENSE`, shipped in the release archives and the image |

## Quick reference

### J1/J2 against a portal
```sh
export PORTAL_URL=https://api-portal.internal
# credentials: PORTAL_TOKEN (static or ID token), or GitHub Actions' ID token
portal check --baseline-from "$PORTAL_URL"   # diff against the latest published versions
portal push --dry-run                         # the portal's own verdict, nothing stored
portal push --output sarif=portal.sarif       # publish; exit 1 if any API is rejected
portal push --ack BRK-OA-29783f --ack-reason "why it is safe"
```
Example workflow: `docs/spec/examples/ci/github-actions.yml`.

### J2, offline (events and OpenAPI)
```sh
git worktree add ../base main
portal check --config portal.config.yaml --baseline ../base/portal.yaml
# a breaking change without a major bump → exit 1 with an id such as
# BRK-CE-811dea (events) or BRK-OA-29783f (OpenAPI)
portal check ... --ack BRK-CE-811dea --ack-reason "why it is safe"
portal bundle --out base/                       # base/<api-id>.tar.zst + content hashes
portal check --baseline orders-http=base/orders-http.tar.zst   # per-API bundle baseline
# CI: text to the log, plus files for annotations, from one run
portal check --baseline ../base/portal.yaml --output sarif=portal.sarif --output junit=portal.xml
# then github/codeql-action/upload-sarif with `if: always()`
portal diff old/events.yaml new/events.yaml     # exit 1 if any change is breaking
portal diff old/openapi.yaml new/openapi.yaml   # the same kind on both sides
```

## Code map

| Package | Role |
|---|---|
| `cmd/portal` | cobra CLI: `init`, `check`, `diff`, `bundle`, `push`, `migrate`, `reindex`, `serve`, `admin token`, `mcp` |
| `internal/check` | Orchestrator: descriptor → per-API parse → lint → (baseline) diff → policy; `RunBundles` does the same for an uploaded descriptor + bundles; `DiffBundles` compares two stored versions |
| `internal/descriptor` | `portal.yaml` load and validation; `Sniff` detects a spec's kind |
| `internal/yamldoc` | YAML with pointer→line index; schema validation; violation flattening |
| `internal/bundle` | `$ref` file closure (remote refs, escapes and missing files are problems); `RelTo`; `Bundle`: `Hash`, `Pack`/`Unpack` tar.zst |
| `internal/spec/eventcatalog` | `events.yaml` → `model.Spec`, plus the compiled payload schemas |
| `internal/spec/openapi` | OpenAPI 3.0/3.1 → `model.Spec` (built from the raw docs, not libopenapi) |
| `internal/model` | `Spec`, `Message`, `Binding`, `Operation`, `Schema`, `Finding`, `Change` |
| `internal/lint` | `CloudEvents`, `OpenAPI` (vacuum + native), `Score` |
| `internal/compat` | `Check(old, new, mode)`, which reduces to the subset test `sub(A,B)`; `Fingerprint` |
| `internal/diff` | `Events` contract diff, `OpenAPI` (oasdiff adapter) |
| `internal/policy` | Semver gate, lifecycle, pre-releases, acks |
| `internal/report` | Text, JSON, SARIF 2.1.0 and JUnit output |
| `internal/config` | `portal.config.yaml`: org prefix, teams, `server` (listen, publicURL), CI issuers, OIDC, admins, `brokers` |
| `internal/httpapi` | `/api/v1`: `POST push`, `POST check` (dry run), `GET apis/{id}/versions/{v\|latest}/bundle`, `/healthz`, `/readyz`; `Authenticator` interface |
| `internal/web` | Web UI: embedded templates and static files (htmx vendored), CSP, OIDC sign-in, sessions, roles; Markdown for agents (`agent.go`); `web/devoidc` is the `--dev-login` stub provider |
| `internal/textdiff` | Line diff per file (go-udiff's `lcs`), both sides' line numbers, `Fold` for unchanged runs |
| `internal/index` | `model.Spec` → index rows and search documents; `Words` splits identifiers |
| `internal/schematree` | JSON Schema → tree (`Node`) for the event page and agentdoc; depth and node limits |
| `internal/agentdoc` | Markdown for AI agents: `Catalogue` (llms.txt), `APIPage` (`full` for llms-full.txt), `OperationPage`, `MessagePage`; `Load` parses a bundle with every document so refs resolve |
| `internal/agentmcp` | MCP tools over a `Catalogue` of Markdown pages: in-process (`web/mcp.go`, `/mcp`) or `Remote` over HTTP with a PAT (`portal mcp`) |
| `internal/initkit` | `portal init`: `Detect` specs (via `descriptor.Sniff`), `Propose` ids, render `Descriptor` and the GitHub `Workflow` (embedded template) |
| `internal/client` | REST client for the CLI: credentials from the environment (`PORTAL_TOKEN`, GitHub Actions ID token), `Push` with retries, `Latest` baseline with hash check |
| `internal/auth` | `CI` authenticator: OIDC ID tokens from `ci.trustedIssuers` (go-oidc, lazy discovery), static `ptk_` tokens by sha256 |
| `internal/store` | Postgres (pgx) repository; embedded goose migrations behind an advisory lock; `Record` stores a push, and indexes a published one, in one transaction; readers `Model`, `MessageRoles`, `TypeConsumers`, `Dependencies`, `Search`; `storetest.SeedAPIs` makes synthetic APIs |

## Tasks

`task build | test | vet | check:examples` (Taskfile, not Make);
`task release -- v0.1.0` and `task image -- v0.1.0` (see [decisions/distribution.md](decisions/distribution.md)).
Postgres: `task dev:db` (podman-compose, port 55432), then `task migrate`,
`task psql`, `task run` (serve on http://localhost:8080 with `--dev-login`: the
stub sign-in page lets you pick a name and groups), `task test:integration`
(store, httpapi and the CLI end to end, one cloned database per test), `task bench:search`
(500 synthetic APIs, logs the query plan); `task dev:db:down` wipes it.
Golden files are regenerated with `go test ./<pkg> -update` (eventcatalog,
openapi, compat).

## Decisions

Decisions taken where the spec was silent are in [decisions/](decisions/README.md),
one file per area. Add to them as you go.

## Next steps

1. **Deploy**, which closes the open "done when"s of M2–M4:
   1. Cut `v0.1.0`: `task release -- v0.1.0`, then
      `gh release create v0.1.0 dist/*`.
   2. Deploy the image (`task image`) with a `portal.config.yaml` that lists
      the real teams, OIDC and brokers.
   3. Pilot teams follow `docs/guide/onboarding.md`. Time each onboarding
      (M4's target is 30 minutes), and note every finding that fires on
      their specs: that data feeds M5's ruleset tuning and the guide's rule
      table.
2. **M5, rollout:** a Helm chart, `warn-until` grace for new errors,
   rulesets from files with severity overrides, and ruleset tuning once
   pilot specs exist.
3. Smaller open items: a `diff_versions` MCP tool (needs a Markdown diff
   page) and a JSON route for schemas past the 200-line cap (see
   [decisions/agents.md](decisions/agents.md)).

## Known gaps

- Real-spec tuning (Q7: `oneOf` usage, Q6: type prefix) is pending access
  to company specs; scheduled for M5.
- **Distribution:** the repo is public at `github.com/elqsar/better-api-portal` (since 2026-10-07), but no release is cut yet;
  releases are built locally and uploaded by hand, with no CI release
  workflow, signing or SBOM. The Helm chart is M5.
- The GitHub OIDC path is tested against a fake issuer only, not a real
  Actions run. Pull requests from forks get no ID token, so they can't use
  `--dry-run` or `--baseline-from`.
- `server.publicURL` unset means push results carry paths, not URLs.
- `portal diff` doesn't take bundles yet, only spec files.
- **Search:** no `GET /api/v1/search` yet (`/api/v1` takes CI credentials
  only; it comes with session reads there or the MCP server), and no
  filter for older versions. After an htmx search the page `<title>` keeps
  the first query.
- **Diff page:** no `GET /api/v1/apis/{id}/diff` yet (same reason as
  search). Very large files aren't capped: a 5 000-line spec renders
  every changed region, which is fine, but a rewritten file shows in
  full.
- AsyncAPI v3 isn't parsed (the kind is recognised and then skipped).
- Retired APIs never release their event types: 04-governance's 90-day
  release needs a retirement date on `apis`, which isn't recorded.
- Rejected versions aren't cleaned up after 30 days yet (needs the jobs
  table).
- There are no rulesets from files, no severity overrides and no
  `warn-until` (M5).
- OpenAPI changes carry no `Field`: oasdiff's arguments aren't parsed into
  one. Changes that aren't tied to an operation point at the document, with
  no line.
- `compat` on OpenAPI component schemas: fragment-only `$ref`s inside a
  component would resolve against the component rather than the file.
  That's irrelevant until OpenAPI schemas go through `compat`; oasdiff is
  planned for OpenAPI instead.
