# Implementation status

Last updated: 2026-09-27

A handoff note for continuing the work. The spec is in [spec/](spec/README.md),
and the roadmap and milestones are in [spec/06-roadmap.md](spec/06-roadmap.md).

## Where we are

Milestone **M1: `portal check` (offline)** is done: its "done when" from
the roadmap (green on `docs/spec/examples/`, a golden test for every row of
the compat table) is met. Event catalogues and OpenAPI both get the full
pipeline: parse, lint, diff against `--baseline`, then the semver gate. Specs
are bundled with a content hash. Reports come as text, JSON, SARIF and JUnit.

**M2: registry + push** is code-complete: store, push API, CI auth,
`portal push`, `check --baseline-from`, an example workflow, and an
end-to-end test of the CLI against a real portal. Its "done when" (a pilot
service pushing from CI) still needs a deployed portal and a way to ship
the CLI (see "Known gaps"). **M3: read UI** is code-complete: all eight steps are done (see "Decisions (M3 UI)"), and the J3–J5
acceptance tests pass. Its "done when" also says engineers find APIs
without asking, which needs a deployed portal, like M2's pilot. Tuning
against real specs moves to M5, as the roadmap schedules it, because no real
specs are available yet. **M4: onboarding kit** is code-complete: `portal
init` (with `--ci github` and `--events-from`), the onboarding guide, and a
J1 acceptance test (see "Decisions (M4 onboarding)"). Its "done when" (3
pilot teams onboard in under 30 minutes each) needs the deployed portal too.

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
| `cmd/portal` | cobra CLI: `init`, `check`, `diff`, `bundle`, `push`, `migrate`, `reindex`, `serve`, `admin token` |
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
| `internal/web` | Web UI: embedded templates and static files (htmx vendored), CSP, OIDC sign-in, sessions, roles; `web/devoidc` is the `--dev-login` stub provider |
| `internal/textdiff` | Line diff per file (go-udiff's `lcs`), both sides' line numbers, `Fold` for unchanged runs |
| `internal/index` | `model.Spec` → index rows and search documents; `Words` splits identifiers |
| `internal/initkit` | `portal init`: `Detect` specs (via `descriptor.Sniff`), `Propose` ids, render `Descriptor` and the GitHub `Workflow` (embedded template) |
| `internal/client` | REST client for the CLI: credentials from the environment (`PORTAL_TOKEN`, GitHub Actions ID token), `Push` with retries, `Latest` baseline with hash check |
| `internal/auth` | `CI` authenticator: OIDC ID tokens from `ci.trustedIssuers` (go-oidc, lazy discovery), static `ptk_` tokens by sha256 |
| `internal/store` | Postgres (pgx) repository; embedded goose migrations behind an advisory lock; `Record` stores a push, and indexes a published one, in one transaction; readers `Model`, `MessageRoles`, `TypeConsumers`, `Dependencies`, `Search`; `storetest.SeedAPIs` makes synthetic APIs |

Tasks: `task build | test | vet | check:examples` (Taskfile, not Make);
`task release -- v0.1.0` and `task image -- v0.1.0` (see "Decisions (distribution)").
Postgres: `task dev:db` (podman-compose, port 55432), then `task migrate`,
`task psql`, `task run` (serve on http://localhost:8080 with `--dev-login`: the
stub sign-in page lets you pick a name and groups), `task test:integration`
(store, httpapi and the CLI end to end, one cloned database per test), `task bench:search`
(500 synthetic APIs, logs the query plan); `task dev:db:down` wipes it.
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
- **Compatible payload changes name their fields** (after M3):
  `compat.FieldChanges` walks `properties`, `required`, `enum` and `items`
  through `$ref`s (16 levels, cycles cut) and reports properties added
  (optional/required), removed, now required/optional, and enum values
  added/removed. Each becomes one additive `ce-payload-changed` with `Field`
  set ("…v1 payload /channel added (optional), compatible (FORWARD)"). A
  change the walk can't describe (constraints, combinators) keeps the one
  generic line. The messages, and so the ids, of compatible changes
  differ from those stored before; they're additive, so no ack refers to
  them.
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
  - `descriptor-broker-unknown` (warn): an environment names a broker
    that isn't in the configuration. Checked by `check --config` too, and
    skipped when the configuration has no brokers.
  - `ce-type-unique` (error) and `ce-topic-single-owner` (warn), server
    only (`httpapi/rules.go`, "portal rules"). Each pushed CloudEvents API
    is compared with every API's latest version (`store.TypeDeclarations`,
    `store.TopicProducers`, one query each per push) and with the other
    APIs in the push; APIs claimed by another repo are skipped (rejected
    anyway).
    - `ce-type-unique`: any other API declaring the type, whatever the
      role, is an error, one finding per other API at the message's
      pointer and line. The message suggests `consumes` in `portal.yaml`,
      the likely mistake. It is strict: a stored API's latest version
      counts even if this push drops the type from it, so moving a type
      between APIs is two pushes. Retired APIs keep their types (the
      90-day release needs a retirement date, which isn't stored).
    - `ce-topic-single-owner`: per API and topic (protocol + address),
      at the first `produces` message bound to it, when an API of another
      owner team (API-level `owner` counts) produces there. Stored rows of
      APIs in this push are replaced by what's pushed; retired APIs don't
      produce.
    - Both count in the pushed API's score (−10 and −2), like lint rules.
    - `TestEventPage`'s duplicate declaration is now written to the
      store directly, since a push can't make one; the banner stays for
      data from before the rule.
- **An `unchanged` push updates the metadata** (owner, lifecycle, sunset,
  `meta`: links, tags, environments, consumes, …) when it has no errors and
  its version is the API's latest, so a descriptor-only change needs no
  version bump. A rebuilt old tag doesn't revert it. `store.UpdateMeta`
  compares in SQL (`IS DISTINCT FROM`, jsonb equality), rewrites
  `dependencies` and audits `push.meta-updated`. The result says
  `"metadataUpdated": true` (also on `--dry-run`, which writes nothing),
  and `portal push` prints "(metadata updated)".
- **The bundle download** carries `X-Portal-Version`,
  `X-Portal-Content-Hash` and `X-Portal-Lifecycle`, so a check can use it as
  a baseline. It needs the same authentication as push, for now.

## Decisions (CI auth)

- **One `Authorization: Bearer` header** for both kinds: a `ptk_` prefix
  means a static token, anything else is verified as an ID token. The issuer
  is read from the unverified `iss` to pick the verifier; only exact
  matches of configured issuers are trusted.
- **Repo = `repoPrefix` + repo claim.** Several issuers must have distinct
  prefixes (config load fails otherwise), so a GitLab project can't push to
  the GitHub repo with the same path. Static tokens use the same CI subject,
  so a repo can switch methods and keep its APIs.
- **Refs gate pushing, not authentication.** A ref outside `allowedRefs`
  (default `refs/heads/main`, `refs/tags/*`, `path.Match` patterns, so `*`
  doesn't cross `/`) still checks and downloads baselines; a push gets 403.
  `Identity.CanPush` carries this. GitLab refs are qualified with
  `ref_type`.
- **Audit:** the actor is the token's `sub` (e.g.
  `repo:acme/orders:ref:refs/heads/main`) or `token:<id>`. The run URL is
  derived for github.com (`run_id`) and GitLab (`pipeline_id`). Static-token
  callers may send `X-Portal-Ref`, `-Commit`, `-Run-URL`, recorded
  unchecked.
- **An unreachable issuer** is a 500, not a 401, and isn't cached, so it's
  retried on the next request.
- **Static tokens:** 32 random bytes, sha256 stored, default 90 days,
  `created_by` and `last_used_at` (migration 00002). `portal admin token
  create|list|revoke`, audited as `token.create`/`token.revoke` with actor
  `cli:<os user>`. `httpapi.NoAuth` is gone; `serve` always runs `auth.CI`.
- go-oidc, go-jose and oauth2 are 3 new modules; the binary grows about
  1.5 MB (94.2 → 95.7 MB).

## Decisions (distribution)

- **Module path** `github.com/elqsar/better-api-portal` (was
  `better-api-portal`), so `go install
  github.com/elqsar/better-api-portal/cmd/portal@vX` works once the repo
  is pushed there and public (or `GOPRIVATE` is set).
- **`portal version`** and `--version`: the `-X main.version` set by
  release builds, else the module version Go stamps (a pseudo-version
  like `v0.0.0-…-8c59a9b49a65+dirty` from a checkout, the tag after `go
  install …@vX`), plus Go version, platform and commit when known.
- **`task release -- vX`** (default `git describe`): `CGO_ENABLED=0`,
  `-trimpath -s -w`, linux and darwin × amd64 and arm64, each
  `dist/portal_<v>_<os>_<arch>.tar.gz` with the binary and `LICENSES.txt`
  (the vendored htmx and Scalar notices), plus `dist/SHA256SUMS`. About
  19 MB per archive. Publishing is manual: `gh release create vX dist/*`.
  Windows isn't built (no CI runner needs it yet).
- **Image** (`Containerfile`, `task image -- vX`): `golang:1.26` build
  stage to `gcr.io/distroless/static-debian12:nonroot` (CA certificates
  for OIDC, no shell, non-root), 69.6 MB. Entrypoint `/portal`, default
  command `serve`, so the same image runs the server or `portal push` in
  CI. `.containerignore` leaves out `.git`, `bin`, `dist`; the version
  comes from `--build-arg VERSION` (`-buildvcs=false`).
  - The default podman machine (3.7 GiB) killed the compiler at full
    parallelism (in vacuum's packages, after 30 min of thrashing), so
    `task image` passes `--build-arg GOFLAGS=-p=2`; the Containerfile
    also mounts a Go build cache. CI runners with more memory can leave
    `GOFLAGS` empty.
  - Checked: `version`, `check` on the mounted example, and `serve`
    against the dev database (`/readyz` 200, UI 503 without
    `oidc.issuer`, unauthenticated push 401).
- **Example workflow:** `PORTAL_CLI_VERSION` pinned in `env`; the job
  downloads the release archive for `$RUNNER_ARCH` and verifies it
  against `SHA256SUMS` before putting it on `PATH`. The install script
  was run against a local `dist/` over `file://`. `go install` and the
  image are given as alternatives in a comment.
- There is no project `LICENSE` yet; the archives carry only third-party
  notices. Go module licenses aren't collected into the archives either.

## Decisions (M4 onboarding)

- **`portal init`** (`internal/initkit`, `cmd/portal/init.go`) searches
  the repo for OpenAPI 3, AsyncAPI 3 and event catalogue files with
  `descriptor.Sniff`. It skips hidden directories, `node_modules`, `vendor`,
  `dist`, `bin`, `build`, `target`, `testdata` and files over 8 MB, and
  reports Swagger 2 and AsyncAPI 2 files with Sniff's conversion hint.
  - **Ids:** `<service>-http` for the only OpenAPI spec and
    `<service>-events` for the only event spec. The service is the directory
    name without a `-service`/`-svc`/`-api` suffix, or `--service`, so the
    orders example gets its real ids back (tested). A kind with several
    specs is named from each title (else the file name). Ids are made valid
    for the id pattern and unique (`-2`, …).
  - **Descriptor:** `--owner` is required (no placeholder that would fail
    later). `lifecycle: production`; commented hints for `system`, `links`,
    `environments` (URL or broker per kind) and `consumes`. The generated
    file passes the descriptor schema (tested).
  - It writes `portal.yaml` (and the workflow with `--ci github`), runs
    `check` on it without a config and prints next steps. Exit 1 when the
    check has errors. Nothing is overwritten without `--force`; `--stdout`
    only prints.
- **`--events-from DIR`** (`initkit.Events`) drafts an event catalogue from
  existing payload schemas (the migration path from CloudEvents + JSON
  Schema).
  - One `produces` message per JSON Schema in DIR (a file with `$schema`,
    `type` or `properties`, and not a spec) that no other schema there
    `$ref`s, so shared parts like `money.json` aren't messages.
  - The type is the schema's `$id` when that is already a name (no URL
    scheme, has a dot). Otherwise it is `--type-prefix` plus the file name
    split into words (dashes, underscores, camelCase) plus `.v<major>`. The
    major comes from the file name (`.v1`) or a URL `$id` (`…/v2`), else 1.
    On the example's schemas this gives exactly the example's types
    (tested), and the draft parses cleanly.
  - `summary` is the schema description's first line, else a TODO;
    `--kafka-topic`/`--nats-subject` set `defaults.bindings`, else a
    commented TODO (and `check` reports the missing binding as an error).
  - It goes next to DIR (`--events-out` overrides it, but it must stay
    inside the root), and portal.yaml includes it as `<service>-events`.
  - **Inventory (Q6):** stderr lists each type, where its name came from,
    and which fail `ce-type-format` or the prefix, with the advice to publish
    a new type alongside rather than rename one producers send. The ack list
    in the roadmap's Q6 default doesn't apply: acks accept breaking changes,
    not lint errors.
- **J1 acceptance test** (`TestJourneyOnboardAService`, in
  `task test:journeys`): it copies the payments fixture without its
  `portal.yaml` and generates the descriptor and workflow with `initkit`
  (the CLI's own flags are tested in `cmd/portal`). It asserts that exactly
  those 2 files were added and that one push publishes both APIs with the
  fixture's real ids, then checks the team's API list (golden
  `j1-apis.html`) and the API page. The generated descriptor has no
  `consumes`, unlike the fixture's.
- **Guide:** `docs/guide/onboarding.md` (a how-to, linked from the spec
  README and printed by `portal init`): install, `init` (with specs, or
  `--events-from`), common findings and their fixes, commit and publish,
  versions and acks, descriptor-only changes, and troubleshooting. Its rule
  table lists the rules that fire most on unlinted specs, from the example
  and fixtures; revisit it with pilot data (M5).
- **CI template:** `internal/initkit/templates/github-actions.yml` is
  embedded; `Workflow` replaces the example's `PORTAL_URL` and
  `PORTAL_CLI_VERSION`. `TestWorkflowExample` keeps
  `docs/spec/examples/ci/github-actions.yml` byte-identical to it. `--ci`
  needs `--portal-url`. `--cli-version` defaults to the binary's own
  version when it is a release (not a pseudo-version or "devel"). GitHub
  only (Q1). The template notes monorepos (`--descriptor`, one job per
  descriptor).

## Decisions (push client)

- **PR jobs use `push --dry-run`** in the example workflow, not
  `check --baseline-from`: it's the server's exact verdict, org rules
  included (`descriptor-owner-unknown` needs the config the service repo
  doesn't have). `check --baseline-from` is for local runs and J2's
  "offline apart from one fetch".
- **Credentials:** `PORTAL_TOKEN` wins, sent as is (so GitLab's `id_tokens`
  named `PORTAL_TOKEN` just works); else GitHub Actions' ID token for
  `--audience`. The run headers (`X-Portal-Ref`, …) are sent with static
  tokens only.
- **Retries:** network errors and 5xx, three times (1s, 2s, 4s); 4xx never.
  A 401 message hints at `id-token: write` / `PORTAL_TOKEN`.
- **Local failures first:** a descriptor or `$ref` closure with errors is
  reported without contacting the portal (exit 1).
- **Paths:** the server's descriptor-relative paths are joined with the
  descriptor's directory, so text and SARIF match `check`'s; the JSON output
  is the push response, with status and URL per API.
- `--baseline-from` skips AsyncAPI APIs, logs each baseline to stderr, and
  verifies the bundle against `X-Portal-Content-Hash`.

## Decisions (M3 UI)

Agreed 2026-09-26; 05-architecture's Storage table and Web UI section are to
be changed in M3's first commit to match.

- **Stack as in D10:** `html/template` + htmx, `embed.FS`, one hand-written
  CSS file (dark mode via `prefers-color-scheme`), no Node. Every page works
  without JS; htmx only swaps tabs, filters and search results. Strict CSP
  (`script-src 'self'`, no `hx-on`).
- **Model storage:** `version_models.model jsonb` (the serialised
  `model.Spec`, already JSON-tagged) for rendering, plus thin relational rows
  only for queries across APIs: `messages(type, role, api_id, version_id)`,
  `operations`, `bindings(protocol, address, …)`, `dependencies`, and
  `search_docs` (tsvector + `pg_trgm`). No `schemas`/`schema_fields` tables.
  Written in `store.Record`'s transaction; `portal reindex` rebuilds all of
  it from the bundles.
- **Scalar** gets one server-bundled document,
  `/apis/{id}/v/{v}/openapi.json` (external `$ref`s resolved with
  kin-openapi), cached by content hash. Scalar's standalone JS is embedded
  and pinned.
- **Raw diff is server-side** (pure-Go Myers diff, e.g. `hexops/gotextdiff`),
  rendered as an HTML table: per-file YAML by default, canonical JSON as a
  "semantic" toggle, unchanged regions folded and expanded with htmx.
  Cacheable forever, since both sides are content hashes. Replaces the
  spec's "small embedded JS diff library".
- **Pages:** `/`, `/search`, `/apis`, `/apis/{id}/versions/{semver}` (tabs:
  overview, docs, versions, lint, dependencies; deprecation banner),
  `/events/{ce-type}` (across APIs), `/apis/{id}/diff?from=&to=`,
  `/teams/{slug}`. `/admin` after M3.
- **Auth:** OIDC code flow + PKCE, sessions in Postgres, roles from group
  mapping; `/api/v1` reads also accept the session cookie.
- **Index (step 1, done):**
  - Only published versions are indexed. Rejected ones stay reachable
    through `versions` and the reports.
  - `latest_version_id` is the highest release, else the highest
    pre-release (the push baseline stays release-only).
  - `dependencies` are per API (its current `consumes` from `apis.meta`),
    not per version, and may point at APIs not in the portal.
  - Search documents: one per API, operation, message and schema.
    `terms` (identifiers split by `index.Words`, weight A) and `body`
    (summaries, descriptions, schema property names, titles and enum
    values, weight B, capped at 32 KB) both use the `english` config, so
    "refunds" stems to "refund". Schema examples and defaults aren't
    indexed.
  - `check.ParseBundle` re-parses a stored bundle into exactly the model
    the push stored (tested), which is what `portal reindex` uses.
  - Descriptor links, environments and consumes got lower-case JSON tags.
    Rows pushed before that keep `{"API": …}` in `apis.meta` until their
    next push; `reindex` doesn't rewrite `meta`.
  - The page URL is `/apis/{id}/versions/{v}`, as `push` already prints,
    not `/v/`.
- **Web skeleton (step 2, done):**
  - `serve` routes `/api/`, `/healthz`, `/readyz` to the REST API and the
    rest to the UI. Without `oidc.issuer` the UI shows what to configure;
    CI keeps working.
  - Sign-in: code flow + PKCE + nonce; discovery is lazy and retried. One
    `portal_login_<state prefix>` cookie per sign-in in progress (10 min),
    because a browser's `/favicon.ico` request used to start a second
    sign-in and overwrite the first (seen only in a real browser; now a
    test). Only page loads (`Sec-Fetch-Dest: document`, or none) are sent
    to sign in; subresources and htmx requests get 401.
  - Sessions: 32 random bytes in `portal_session` (HttpOnly, SameSite=Lax,
    Secure when publicURL is https), sha256 in `sessions`, 12 h absolute;
    expired rows are deleted at each sign-in. Groups are stored at sign-in,
    so role changes apply at the next sign-in.
  - Non-GET requests with a foreign `Origin` (or `Sec-Fetch-Site:
    cross-site`) get 403.
  - CSP `default-src 'self'`, no inline script or style; htmx configured
    by meta tag with `allowEval: false` and no indicator styles. No
    `hx-boost`: a boosted fetch can't follow a redirect to the identity
    provider.
  - Static files: `/static/<name>?v=<hash>` cached for a year, otherwise
    `no-cache` with an ETag.
  - `--dev-login` stub: issuer `<publicURL>/dev/oidc`, client
    `portal-dev`, offers every configured team group plus the admins
    group; redirects only to the portal's own host and requires S256
    PKCE. publicURL defaults to `http://localhost:<port>` with it.
  - `/api/v1` reads don't accept the session cookie yet; that comes with
    the docs step, which is the first to need it.
- **API list and pages (step 3, done):**
  - URLs: `/apis` (filters `team`, `kind`, `lifecycle`, `tag`, `q`),
    `/apis/{id}` → latest, `/apis/{id}/versions/{v}` (overview; `latest`
    redirects), `…/{v}/lint`, `/apis/{id}/versions` (history). Each tab
    is its own URL; htmx swaps `#api-body` and pushes the URL, and the
    list swaps `#api-table` with a cleaned `HX-Push-Url`.
  - Only APIs with a published version are listed; order is in use,
    deprecated, retired, then id. `q` is a case-insensitive substring of
    id or title, with LIKE wildcards escaped.
  - Rejected pushes appear in the history for the owning team and
    admins only, with error counts and the CI run link; their findings
    aren't shown in the UI (they're in the CI log).
  - "Lint & changes" is one tab: findings sorted by severity, then the
    diff against the baseline, with ack reasons inline.
  - Descriptions render as plain text (`white-space: pre-line`), not
    Markdown, for now.
  - Web pages are tested against Postgres (`pages_integration_test.go`),
    publishing through the real push API; the in-memory store only
    covers sign-in.
- **Docs (step 4, done):**
  - `/apis/{id}/versions/{v}/openapi.json` is a UI route (session auth),
    not `/api/v1`: the REST API still takes CI credentials only.
    `openapi.Document` loads the bundle with kin-openapi from memory,
    then `InternalizeRefs`; 3.1 round-trips. ETag = content hash +
    `documentVersion` (bump it when the output changes); 32 documents
    cached in memory.
  - Scalar 1.72.1 `standalone.js` is vendored as `static/scalar.js`
    (4.4 MB, served gzipped at 1.3 MB); the binary is now 100.9 MB. To
    upgrade: replace the file, update `LICENSES.txt`, and recheck
    `docs.js`'s options against the new version (fonts, telemetry,
    agent, MCP, `externalUrls`).
  - The Docs page's CSP adds `style-src 'nonce-…'` with a
    `<meta property="csp-nonce">`, which Scalar reads for its injected
    stylesheet. Scripts stay `'self'` only.
  - `app.css` is `@layer portal { … @scope (body) to (.scalar-app) { … } }`.
    New rules go inside the scope unless they must style `body`.
  - The Docs tab is a full page load (no `hx-get`). The mount point must
    stay empty (Scalar hydrates otherwise).
  - Scalar's API client ("Test request") is hidden: the CSP would block
    calls to the API's servers anyway.
- **Event page (step 5, done):**
  - `/events/{type}`: the declaring APIs come from `store.MessageRoles`
    (latest versions only), the consumers from `store.TypeConsumers`:
    `consumes` entries naming the type, or naming a declaring API with no
    `types`. 404 only when neither exists; a type that is only consumed
    renders with a "no producer published" banner.
  - Any second declaration, whatever its role, is a `ce-type-unique`
    conflict (03-formats: `receives` owns the contract too). The page
    shows the first (produces before receives, then by id) under an error
    banner listing the others.
  - Attributes, bindings and examples come from `version_models`. The
    schema documents aren't in it (`Schema.Doc` is `json:"-"`), so the
    tree re-parses the bundle with `check.ParseBundle`; parsed specs are
    cached by content hash (the generic `cache[V]`, 32 entries, shared
    with the OpenAPI documents). If that fails, the rest of the page still
    renders, with the error in place of the tree.
  - The tree (`web/schema.go`) is nested `<details>`, no JS, two levels
    open. `$ref`s resolve through `compat.Schema` (new exported `Top`,
    `Follow`, `Node.Child`), so it follows the same bundle rules as the
    compat checker. Properties are sorted by name (maps lose the file's
    order). Recursion stops with a note; 16 levels and 1,500 nodes at
    most. The referring schema's description wins over the target's.
  - Bindings link to a broker's `ui` for each of the owning API's
    environments on a broker of the binding's protocol. Binding props
    show as strings, or compact JSON for anything else.
  - `brokers` in `portal.config.yaml`: `name` (unique, required),
    `protocol`, `ui` (URL); protocol fields (`bootstrap`, `account`, …)
    are kept raw in `Settings`. The example config's kafka-prod got a `ui`.
- **Search (step 6, done):**
  - `store.Search(SearchQuery{Q, Kind, Team, API, Limit})`: candidates are
    a `UNION` of index-backed branches (tsquery on `tsv`; `title %> q`,
    i.e. `word_similarity`; `ILIKE` for queries under 3 characters), limited
    to latest versions of APIs that aren't retired. An `OR` would use
    neither GIN index.
  - Rank = `ts_rank_cd` + 2 × title word similarity + 1 for an exact title
    + 0.1 for the API's own document, × 0.5 when deprecated. Ties by api
    id, kind, ref.
  - `pg_trgm.word_similarity_threshold` is 0.45 (default 0.6 misses
    "refnd" → "refund", 0.5), set per transaction.
  - **`plan_cache_mode = force_custom_plan`** in the same transaction: pgx
    prepares the statement, and after five runs Postgres chose a generic
    plan that can't prune the UNION branches. p95 at 500 APIs went from
    6.2 s to 61 ms (limit 300 ms). Keep it if the query changes.
  - Snippets: `ts_headline` over `body`, for the returned rows only, with
    `\x01`/`\x02` delimiters that `web.highlight` turns into `<mark>`
    after escaping (ts_headline also drops tag-like text). The web layer
    collapses repeated adjacent words (the index holds identifiers as
    written and split) and hides a hit's snippet that marks nothing.
  - UI: `/search?q=&kind=&team=&api=`, 200 hits grouped by API in order
    of each API's best hit; the API's own document is the group heading;
    5 hits per group with "N more in …" (`api=` narrows to one API). Links:
    messages → `/events/{type}`, OpenAPI operations and schemas → the Docs
    tab, the rest → the version overview. htmx swaps `#search-results`
    as you type (250 ms) with a cleaned `HX-Push-Url` (`pushURL`, shared
    with `/apis`). A search box sits in the top bar except on home and
    search; hidden under 50rem.
  - Benchmark (`storetest.SeedAPIs`: 500 APIs, two versions each, 20
    operations or messages and 10 schemas, every 10th deprecated, every
    25th retired): 13–27 ms per search on the dev machine;
    `TestSearchLatency500` (in `task test:integration`, skipped with
    `-short`) fails at p95 ≥ 300 ms and logs the plan.
  - Not done: `GET /api/v1/search` and the older-versions filter (see
    "Known gaps").
- **Diff page (step 7, done):**
  - `/apis/{id}/diff?from=&to=&view=changes|raw&mode=yaml|json&full=1`.
    `to` defaults to the latest, `from` to the published version below
    `to` by semver. Any two published versions can be compared, either
    way round (a note says when `from` is the later one). One version,
    the same content, or a `to` with nothing below it gives a message,
    not an error; an unknown version is a 404.
  - **Changes** come from `check.DiffBundles`: both bundles are unpacked
    side by side and go through the same `diff.Events` / `diff.OpenAPI`
    as a baseline check (`DiffFiles` now shares `diffSpecs`, which takes
    each side's root, so `$ref`s outside the entry's directory resolve).
    The API's current `compatibility` applies. Sorted breaking, warn,
    additive, docs. These are recomputed, so they can differ from the
    push's stored report if the rules changed since; the lint tab keeps
    showing the stored one.
  - **Raw diff** (`internal/textdiff`): go-udiff's `lcs.DiffLines` (gopls'
    algorithm), not the "hexops/gotextdiff" the plan named: that one is
    archived, and go-udiff was already in the module graph (unlinked); it
    adds 0.1 MB. Per file, unchanged files left out, added/removed files
    marked. "Canonical JSON" is each file as the content hash sees it
    (sorted keys, no comments), indented. A missing final newline isn't a
    change.
  - Folding: unchanged runs keep 3 lines of context and fold only if that
    hides more than 2 lines. A fold is its own `<tbody>`; htmx fetches
    `/apis/{id}/diff/lines?from&to&mode&file&start&end` (indices into the
    file's lines) and swaps it, cached `immutable`. Without JS the link
    is the page with `full=1`.
  - Everything for a pair (changes, both raw forms) is computed once and
    cached in memory by the two content hashes and the compatibility mode
    (`cache[*versionDiff]`, 32 entries).
  - Signs are drawn with CSS `::before`, so copying lines copies only
    their text.
  - Linked from the versions tab (a compare form, and each "from X" in
    the Changes column) and the lint tab ("Compare the files").
- **Acceptance tests (step 8, done):**
  - `internal/web/journeys_integration_test.go`: one test per journey,
    each step's page loaded as a signed-in user and checked for what the
    journey names, then its `<main>` compared with
    `testdata/journeys/*.html`. Only `<main>` is kept (the `<head>`
    carries static-file hashes that change with every CSS edit); blank
    lines and trailing spaces are dropped and push times replaced by
    `<time>`. Checked stable over repeated runs. `task test:journeys --
    -update` rewrites them; review the diff like any golden file.
  - `testdata/payments-service` (team-payments, repo `acme/payments`) is
    the second service the journeys need: `payments-http` with `POST
    /orders/{orderId}/refunds` and `Refund` schemas for J3's "refund",
    `payments-events` (`…payment.captured.v1`, `…refund.issued.v1`),
    consuming `orders-events`' `order.created.v1` for J4. It was kept out
    of `docs/spec/examples/` so the example's pinned hashes, scores and
    parser goldens stay as they are. The type names use the payments
    prefix, not J3's `com.acme.orders.refund.issued.v1`.
  - The test site pushes as any repo (`pushDir(repo, dir, edits…)`, one
    static token per repo).
  - J5 publishes a major (2.0.0) that removes a type (breaking), adds a
    payload field (additive) and rewords a summary (docs). Found and
    fixed: a removed message type linked to its event page, which is a
    404 once nothing declares it.
  - J3's latency target stays with `TestSearchLatency500`.
- **API title** (after M3): the descriptor's `title`, else the latest
  version's spec title (`version_models.model->>'title'`), in `ListAPIs`
  (shown and matched by `q`) and `APIDetail`; `APIDetail.Name()` falls back
  to the id. The heading and the page `<title>` use it; the crumbs keep the
  id.
- **Consumers by service** (after M3): `consumes` stays per descriptor
  (decided 2026-09-27; no per-API `consumes`). `store.Dependency` carries the
  consuming API's repo and owner, and `web.byRepo` groups the event page's
  "Consumed by" and the dependencies tab's "Used by" into one entry per
  repo: its CI subject, owner teams (linked to `/apis?team=`), the types
  (or "everything from"), and its APIs. The event page leaves out the types,
  which would only name the page's own type.
- **Commit order:** (1) migration + indexing + `reindex` (done); (2) `internal/web`
  skeleton + login (done); (3) API list/page (done); (4) Scalar docs (done); (5) event page (done);
  (6) search with a 500-API benchmark (done); (7) diff page (done); (8) J3–J5 acceptance
  tests with golden HTML (done).

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

M1 is done. **M2, M3 and M4 are code-complete** (rows 11–24 and 31–34).
Their remaining "done when"s are all operational, and all wait on the same
thing, a deployed portal:
1. Push the repo to `github.com/elqsar/better-api-portal` and cut `v0.1.0`
   (`task release -- v0.1.0`, then `gh release create v0.1.0 dist/*`).
2. Deploy the image (`task image`) with a `portal.config.yaml` that lists the
   real teams, OIDC and brokers.
3. Pilot teams follow `docs/guide/onboarding.md`. Time each onboarding (M4's
   target is 30 minutes), and note every finding that fires on their specs:
   that data feeds M5's ruleset tuning and the guide's rule table.

Also done since M3: descriptor-only changes don't need a version bump (row
27), consumers are grouped by service (28), compatible changes name their
fields (29), and API titles come from the spec (30).

Next in the code: **M5, rollout**, which covers a Helm chart, `warn-until`
grace for new errors, and ruleset tuning once pilot specs exist. A good
candidate before it is the gap in the next section that pilots will hit
first: search documents keep the old title and tags after a descriptor-only
change.

## Known gaps

- Real-spec tuning (Q7: `oneOf` usage, Q6: type prefix) is pending access
  to company specs; scheduled for M5.
- **Distribution:** nothing is published yet (the repo has no remote);
  releases are built locally and uploaded by hand, with no CI release
  workflow, signing or SBOM. No project `LICENSE`. The Helm chart is M5.
- The GitHub OIDC path is tested against a fake issuer only, not a real
  Actions run. Pull requests from forks get no ID token, so they can't use
  `--dry-run` or `--baseline-from`.
- `server.publicURL` unset means push results carry paths, not URLs.
- `portal diff` doesn't take bundles yet, only spec files.
- A descriptor-only change reaches `apis.meta` and `dependencies` (see
  "Decisions (server)"), but not the search documents: `search_docs` keep the
  title and tags of the version's own push until the next version or
  `portal reindex`.
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
