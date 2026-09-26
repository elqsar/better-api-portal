# Implementation status

Last updated: 2026-09-26 · Last commit: `bb9627a`

A handoff note for continuing the work. The spec is in [spec/](spec/README.md),
and the roadmap and milestones are in [spec/06-roadmap.md](spec/06-roadmap.md).

## Where we are

Milestone **M1: `portal check` (offline)** is done: its "done when" from
the roadmap (green on `docs/spec/examples/`, a golden test for every row of
the compat table) is met. Event catalogues and OpenAPI both get the full
pipeline: parse, lint, diff against `--baseline`, then the semver gate. Specs
are bundled with a content hash. Reports come as text, JSON, SARIF and JUnit.

Next is **M2: registry + push**, starting with the Postgres store. Tuning
against real specs moves to M5, as the roadmap schedules it, because no real
specs are available yet.

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

### J2, today (events and OpenAPI)
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
| `cmd/portal` | cobra CLI: `check`, `diff`, `bundle`, `migrate`, `serve` |
| `internal/check` | Orchestrator: descriptor → per-API parse → lint → (baseline) diff → policy; `RunBundles` does the same for an uploaded descriptor + bundles |
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
| `internal/config` | `portal.config.yaml`: org prefix, teams, `server` (listen, publicURL) |
| `internal/httpapi` | `/api/v1`: `POST push`, `POST check` (dry run), `GET apis/{id}/versions/{v\|latest}/bundle`, `/healthz`, `/readyz`; `Authenticator` interface |
| `internal/store` | Postgres (pgx) repository; embedded goose migrations behind an advisory lock; `Record` stores a push in one transaction |

Tasks: `task build | test | vet | check:examples` (Taskfile, not Make).
Postgres: `task dev:db` (podman-compose, port 55432), then `task migrate`,
`task psql`, `task run` (serve on :8080), `task test:integration`
(store and httpapi, one cloned database per test); `task dev:db:down` wipes
it.
Golden files are regenerated with `go test ./<pkg> -update` (eventcatalog,
openapi, compat).

## Decisions taken where the spec was silent

- **`--baseline`** can be repeated. It takes at most one previous
  `portal.yaml`, with APIs matched by id, plus any number of
  `api-id=bundle.tar.zst`, which take precedence. Bundles carry no lifecycle,
  so `lifecycle-reversal` isn't checked against them. They're unpacked into
  a temporary directory that is removed after the run.
- **Content hash** (see 05-architecture, "Bundle format"): sha256 over paths
  plus canonical JSON. Formatting and comments don't count, so a change to
  those alone no longer demands a version bump. That matches the server's
  immutability by hash. `TestRunExample` pins the example's hashes, so a
  change to the canonical form fails loudly. The manifest sits at
  `.portal/manifest.json`, so spec files may not live under `.portal/`.
- **Compat footnote.** "The other side's `additionalProperties: false`" is read
  as the side that must *accept* the instance.
- **Unsupported keywords** are compared raw. Annotations are ignored only at
  the level being compared, so a nested change inside `oneOf` counts
  (conservative). `pattern` and `format` changes are always breaking.
- **`ce-type-major-matches-schema`** reads the major version from the schema
  file name (`x.v1.json`) or from `$id`.
- **Change impact:** a compatible payload change is *additive*;
  annotation-only changes are *docs*. Extension requirement changes depend on
  the role (produces vs receives).
- **Change ids** are `BRK-CE-` plus 6 hex digits of sha256(rule, type, field,
  message), so they're stable for identical input.
- **A broken baseline** is exit 2 (it can't run), not a finding.
- **OpenAPI security:** `security: []` needs `x-portal-public`, and
  `[{}]` counts as public. `sec-auth-responses` requires 401 (or 4XX), not
  403.
- **`org-operation-id`** checks camelCase only. Missing or duplicate ids are
  left to vacuum's rules, so they aren't counted twice.
- **OpenAPI diff levels:** oasdiff `ERR`/`WARN`/`INFO` become
  breaking/warn/additive. Two levels are overridden to match the governance
  table: removing an optional response field is breaking, and a new response
  enum value is a warning. oasdiff's version checks are dropped, since
  `internal/policy` owns semver. The kin-openapi loader only reads files in
  the bundle closure. The ids are `BRK-OA-`, using the same hash as the events
  ids. oasdiff costs about 6 MB (80.2 → 86.3 MB with a plain `go build`) and
  56 modules (145 → 201).
- **Native OpenAPI schema rules** only cover the entry file. vacuum covers
  external files.
- **vacuum stays** (decided 2026-09-26). It costs a lot: the binary grows
  from 6.2 MB to 69.3 MB, and the module graph from 14 to 145 modules. We
  accept that because D9 needs Spectral-format rulesets and the M2 server
  lints too. `openapi-default` drops `oas3-missing-example` and
  `component-description`, which made 12 of the 13 warnings on the example.
  orders-http now scores 94, up from 70. The spec's "96/100" was vacuum's own
  score.

## Decisions (push pipeline)

- **`check.RunBundles`** writes the uploaded descriptor (as `portal.yaml`)
  and every bundle into one temporary root, then calls `Run`, so the server's
  verdict equals `portal check`'s by construction (tested on the example).
- A push must have exactly one bundle per non-AsyncAPI API, with the
  descriptor's `spec` as its entry. Bundles may share a file only with
  identical bytes. Otherwise it's an error (a malformed request), not a
  finding.
- Report paths are relative to the descriptor. Files from a baseline read
  `baseline:<id>/<path>`, and temporary paths inside messages are rewritten
  the same way.
- **`Options.BaselineBundles`** are per-API in-memory baselines that carry the
  previous lifecycle, so `lifecycle-reversal` works against the store, unlike
  bundle files. An id can't have a baseline both there and in `--baseline`.

## Decisions (server)

- **Push request:** multipart, with parts `descriptor`, `bundle:<api-id>`
  (tar.zst) and optional `acks` (JSON object, id → non-empty reason). Limits:
  64 MB per request, 1 MB for the descriptor, and the bundle limits.
- **Push response:** always 200 once processed, with the
  `portal check --format json` shape plus `status` and `url` per API.
  Statuses:
  - `published`, `unchanged` (the same version and hash, e.g. a CI retry),
    `rejected`, `skipped` (AsyncAPI);
  - `accepted`, from `/check` only.
  - 400 for a malformed push, 401 for an unauthenticated one, 413 for one
    that is too large.
- **Claims** follow 03-formats: only a *successful* push claims an id.
  - A rejected first push stores nothing, so another repo can still take the
    id.
  - A rejected push to an API this repo already owns is stored as
    `rejected`.
  - A push to another repo's API gets an `api-claimed` error, which is
    audited as `push.claim-rejected`.
- The **baseline** is the store's latest published non-pre-release version,
  with the API's current lifecycle.
- **A published semver with a different hash** is a `version-immutable`
  error, and nothing is stored.
- A version that was published once always answers a push of the same hash
  with `unchanged`, even if the rules have changed since.
- A descriptor-level error (API `""`) rejects every API in the push.
- **New rules:**
  - `descriptor-owner-unknown` (error): the owner isn't a team in the
    configuration. It's checked by `check --config` too, and skipped when the
    configuration has no teams.
  - `consumes-unknown-api` (warn): server only, and APIs in the same push
    count as known.
- **The bundle download** carries `X-Portal-Version`,
  `X-Portal-Content-Hash` and `X-Portal-Lifecycle`, so a check can use it as
  a baseline. It needs the same authentication as push, for now.

## Decisions (store)

- **Tables in M2** are only what push and the gate need: teams, repos, apis,
  bundles, versions, lint/diff reports, CI tokens, audit. The normalised
  model rows, `search_docs`, `dependencies` and `jobs` come with M3; they can
  be rebuilt from the stored bundles.
- **Immutability** is a partial unique index on `(api_id, semver)` for
  published versions only, so a rejected push can be fixed and retried
  under the same version.
- **Claims** are made by a published push's own insert of the `apis` row
  (`ON CONFLICT … WHERE repo_id matches`), so a claim race can't give an id
  two owners. A rejected push claims nothing (`ErrUnclaimed` if the API
  doesn't exist) and changes no metadata.
- **Latest** is chosen by semver in Go (`x/mod/semver`), not by insertion
  order.
- pgx + goose cost 6.8 MB (87.4 → 94.2 MB) and 8 linked modules (76 → 84).
  `modernc.org/sqlite` in the binary comes from vacuum (`pb33f/doctor`), not
  goose.

## Decisions (reports)

- **JUnit failures match the exit code.** Errors are failures, and warnings
  are too only with `--strict`. Other findings are passing test cases, with
  their line in `<system-out>`. Each API also gets a passing
  `version … score …` case listing its changes.
- **SARIF locations:**
  - Paths are relative to the working directory (`%SRCROOT%`), so run
    `check` from the repository root.
  - A file outside it, such as a baseline's, is replaced by the descriptor
    (line 1), with "(at file:line, outside the checkout)" in the message.
  - If the descriptor is outside it too, locations are absolute `file://`
    URIs.
  - `partialFingerprints["portal/v1"]` ignores line numbers.
  - The output validates against the official 2.1.0 schema; checked by hand,
    not in the tests, which would need the network.
- `--output` values are validated before the run, and the files are written
  even when the check fails.

## Next steps

**M2**, one commit per step (CI auth targets GitHub Actions first, per the
Q1 default):

1. ~~`internal/store`~~ done: pgx + goose, `portal migrate`, `task dev:db`.
2. ~~`internal/check` over in-memory bundles~~ done: `RunBundles`,
   `Options.BaselineBundles`.
3. ~~`portal serve` + push, check, bundle download~~ done; auth is
   `httpapi.NoAuth` (every push gets 401) until step 4.
4. `internal/auth`: GitHub Actions OIDC (`ci.trustedIssuers`, `allowedRefs`),
   claims, fallback static tokens (`portal admin token create`).
5. `portal push` (ID token or `PORTAL_TOKEN`), `check --baseline-from URL`, a
   GitHub Actions example workflow.
6. End-to-end test: publish, then reject a breaking change, then a no-op
   retry.

## Known gaps

- Real-spec tuning (Q7: `oneOf` usage, Q6: type prefix) is pending access
  to company specs; scheduled for M5.
- `portal diff` doesn't take bundles yet, only spec files.
- AsyncAPI v3 isn't parsed (the kind is recognised and then skipped).
- Rules that need the server aren't implemented yet: `ce-type-unique` and
  `ce-topic-single-owner` (they need the model rows, M3).
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
