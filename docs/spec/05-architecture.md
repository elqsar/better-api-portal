# 05 — Architecture

## Shape

One Go binary, `portal`, with subcommands. It needs one Postgres. No other runtime dependencies: no search engine, queue, object store or Node server.

```
                ┌─────────────────────── portal serve (N replicas) ───────────────────────┐
 Browser ──OIDC─►  Web UI (html/template + htmx, Scalar for OpenAPI docs)                  │
                │        │                                                                 │
 CI ──OIDC JWT──►  REST API /api/v1  ──►  Ingest pipeline ──► Store (pgx) ──► Postgres     │
 (portal push)  │                           parse → normalise → lint → diff → gate          │
                │  Worker (same process): re-lint jobs via Postgres SKIP LOCKED queue       │
                └──────────────────────────────────────────────────────────────────────────┘

 Dev laptop / PR:  portal check  ── same parse/lint/diff packages, local files
                                   (+ GET baseline from the portal)
```

### Subcommands
| Command | Purpose |
|---|---|
| `portal serve` | Web UI, REST API and background worker. Runs migrations at start, behind a Postgres advisory lock. |
| `portal check [--descriptor portal.yaml] [--baseline file] [--format text\|json\|sarif\|junit]` | Validate, lint, bundle and diff locally. Doesn't write anything. |
| `portal push` | `check`, then upload. Prints the version URL and report. |
| `portal bundle --out dir` | Write each API's bundle to `<dir>/<api-id>.tar.zst` and print its content hash. It can be used as `check --baseline api-id=file`. |
| `portal diff <old> <new>` | Diff any two spec files or portal refs (`orders-http@2.3.0`). |
| `portal migrate` | Run DB migrations explicitly, for pipelines that prefer that. |
| `portal admin …` | Transfer an API claim, delete a version, issue a fallback push token. |

## Code layout

```
cmd/portal/            main, cobra-style subcommand wiring
internal/
  descriptor/          portal.yaml parsing + validation (embeds schemas/portal.schema.json)
  bundle/              $ref closure, canonicalisation, sha256, tar.zst pack/unpack
  spec/
    openapi/           libopenapi → model
    asyncapi/          AsyncAPI v3 → model (own decoder, validated against official JSON Schema)
    eventcatalog/      events.yaml → model (embeds schemas/eventcatalog.schema.json)
  model/               normalised entities (02-domain-model)
  lint/                vacuum motor for OAS/AsyncAPI; native rules for model/event catalogue
  diff/                oasdiff adapter; event contract diff
  compat/              JSON Schema structural compatibility checker (04-governance §2)
  policy/              baseline selection, semver gate, lifecycle, acks → final verdict
  store/               Postgres repository, migrations (goose), job queue
  search/              tsvector + pg_trgm indexing and queries
  auth/                OIDC (users), CI token verification, fallback tokens, RBAC
  httpapi/             REST handlers (+ OpenAPI spec for the portal itself)
  web/                 templates, static assets, embed.FS
  config/              portal.config.yaml loading
```

The pipeline packages (`descriptor` through `policy`) must not import `store`, `httpapi` or `auth`. This keeps `portal check` a pure function of files, so it can run offline, and makes the pipeline easy to test with golden files.

## Ingest pipeline (push)

1. **CLI:** read the descriptor. For each API, compute the `$ref` closure of its spec, canonicalise it (YAML/JSON parsed, then re-serialised with sorted keys for hashing only; the original bytes are what's stored), hash it, and pack a tarball with a manifest.
2. **CLI → server:** `POST /api/v1/push` with multipart parts `descriptor` and `bundle:{api-id}`.
3. **Server:**
   1. Authenticate the caller, which gives a repo identity.
   2. For each API: check the claim (is this repo allowed to push this id?). If the id is unclaimed, claim it.
   3. Unpack, parse and normalise.
   4. Idempotency: if `{api, semver}` exists with the same hash, return the existing result.
   5. Select the baseline and load its model.
   6. Lint → diff → policy verdict (acks applied).
   7. **In one transaction:** upsert the API metadata from the descriptor, insert the version, model rows, reports and the audit entry, and update the search index.
   8. Respond per API with `published` or `rejected`, the findings, and the URLs.

APIs in one push are processed independently: one can be rejected while another publishes. The CLI exits non-zero if any API was rejected.

Target: < 5 s server time for a 5 000-line spec at MVP scale.

### Bundle format
- **Content hash:** `sha256:` followed by the hex of sha256 over the entry
  path, then, for each file in path order, its path and its canonical JSON.
  Each part ends in a NUL byte. Canonical JSON is the parsed YAML or JSON,
  re-encoded with sorted keys and no HTML escaping. Comments, formatting and
  key order don't change the hash; values and file paths do. Paths are slash
  paths relative to the descriptor's directory.
- **tar.zst:** `.portal/manifest.json` comes first:
  `{format: 1, entry, content_hash, files: [{path, size, sha256}]}`, where
  each `sha256` is of the file's raw bytes. The spec files follow in path
  order. They're stored as the original bytes, with mode 0644, mtime 0 and
  uid/gid 0, so packing the same input always gives the same bytes. The
  manifest describes content only, not API metadata, which keeps
  deduplication by hash sound.
- **Unpacking rejects:**
  - absolute paths, `..`, backslashes, `.portal/` spec paths and duplicates;
  - non-regular entries, and files missing from the manifest or not listed
    in it;
  - a per-file sha256 or content hash that doesn't match;
  - more than 10 MB per file or 50 MB in total.

## Storage (Postgres ≥ 15)

| Table | Key columns |
|---|---|
| `teams` | Synced from config at startup |
| `repos` | `url`, `ci_subject` (OIDC `sub` / repository claim) |
| `apis` | `id` PK, `kind`, `owner`, `lifecycle`, `sunset`, `repo_id`, `meta jsonb` (tags, links, system, environments) |
| `versions` | `(api_id, semver)` unique, `content_hash`, `status`, `source jsonb`, `acks jsonb`, `created_at` |
| `bundles` | `content_hash` PK, `data bytea` (tar.zst), `size`. Content-addressed and deduplicated. |
| `operations`, `messages`, `bindings`, `schemas`, `schema_fields` | FK → `versions`. These are the normalised model rows. |
| `dependencies` | `from_api`, `to_api`, `types text[]` |
| `lint_reports`, `diff_reports` | FK → `versions`, `ruleset_hash`, `score`, `findings jsonb` |
| `search_docs` | `kind`, `ref`, `api_id`, `title`, `body`, `tsv tsvector`, trigram index on `title` |
| `jobs` | `kind`, `payload`, `run_after`, `attempts`. Workers poll with `FOR UPDATE SKIP LOCKED`. |
| `audit_log` | `actor`, `action`, `target`, `details jsonb`, `at` |

Sizing: 500 APIs × 50 versions × ~200 KB compressed is about 5 GB worst case, and realistically well under 1 GB. `bytea` is fine at that size, and the `bundles` table sits behind a small interface so S3/GCS can be swapped in later if needed.

Search is tuned to show only **latest** versions by default. Older versions are searchable with a filter.

## Auth

### Users: OIDC
- Authorization code flow with PKCE (`coreos/go-oidc`, `golang.org/x/oauth2`). Server-side sessions in Postgres, sent as a secure `HttpOnly` cookie.
- **Roles:**

  | Role | Who | Can |
  |---|---|---|
  | viewer | Any authenticated user | Read everything |
  | owner | Members of the team's OIDC group (mapped in config) | Their APIs: re-run lint, view rejected pushes, manage acks |
  | admin | A configured admin group | Everything, plus claims, deletions and tokens |

- All APIs are visible to every employee. Per-API visibility restrictions are an open question ([06-roadmap](06-roadmap.md#open-questions)).

### CI: federated OIDC tokens (preferred)
- **GitHub Actions:** the job requests an ID token with `audience: api-portal`. The portal verifies it against the `token.actions.githubusercontent.com` JWKS and maps the `repository` claim to a repo. Pushes are allowed only from configured refs (default: the default branch and tags).
- **GitLab CI:** `id_tokens:` with `aud: api-portal`, verified against the GitLab instance's JWKS, using the `project_path` and `ref` claims.
- There are no long-lived secrets in CI. Trusted issuers and claim mappings are configured by an admin.

### CI: fallback static tokens
These are for CI systems without OIDC. An admin issues a token per repo (`portal admin token create --repo …`). Only its hash is stored, it expires by default after 90 days, and its use is audited.

`portal check` needs only **read** access: any valid CI token or user token, or none with `--baseline`.

## Portal configuration

`portal.config.yaml` is mounted from a ConfigMap and managed via GitOps. Secrets come from the environment.

```yaml
org:
  name: Acme                       # display only
  eventTypePrefix: com.acme.       # enforced by ce-type-prefix
teams:
  - { slug: team-orders, name: Orders, oidcGroup: eng-orders }
admins: { oidcGroup: eng-platform }
oidc:
  issuer: https://login.acme.internal
  clientId: api-portal
ci:
  trustedIssuers:
    - { issuer: https://token.actions.githubusercontent.com, audience: api-portal, repoClaim: repository, allowedRefs: ["refs/heads/main", "refs/tags/*"] }
brokers:
  - { name: kafka-prod, protocol: kafka, bootstrap: kafka.prod.internal:9093, ui: https://kafka-ui.internal/prod }
  - { name: nats-prod,  protocol: nats,  url: nats://nats.prod.internal:4222 }
  - { name: aws-prod,   protocol: sns,   account: "123456789012", region: eu-west-1 }
rulesets:
  openapi: { default: openapi-default, dir: /etc/portal/rulesets }
```

Nothing company-specific lives in code. This file is the whole customisation surface, which is what makes open-sourcing realistic.

## Web UI

- **Server-rendered** `html/template` with **htmx** for interactivity, embedded with `embed.FS`. There's no Node toolchain for contributors to install and no separate frontend deployment.
- **OpenAPI reference docs:** the **Scalar** API reference web component, loaded from embedded static files and fed the version's raw bundle URL.
- **Events:** our own templates. They show CE attributes, bindings (linked to the broker UI from config), a JSON Schema tree (collapsible, server-rendered), examples, and producer/consumers.
- **Diff view:** a structured list plus a raw side-by-side text diff (a small embedded JS diff library).
- Pages: Home/search · API list (filter by team, kind, lifecycle, tag) · API page (overview, docs, versions, lint, dependencies) · Message page · Diff page · Team page · Admin.

## Portal's own REST API

`/api/v1` is described by `internal/httpapi/openapi.yaml`. The portal pushes that spec to itself on startup as `api-portal-http`, so it's the first API in the catalogue.

| Method & path | Purpose |
|---|---|
| `POST /api/v1/push` | Upload a descriptor and bundles (CI) |
| `POST /api/v1/check` | Server-side dry run with the same response as push; nothing is stored |
| `GET /api/v1/apis` · `/apis/{id}` | List and get APIs |
| `GET /api/v1/apis/{id}/versions[/{v}]` | Versions |
| `GET /api/v1/apis/{id}/versions/{v}/bundle` | Raw bundle (used by `check` as the baseline) |
| `GET /api/v1/apis/{id}/diff?from=&to=` | Diff report |
| `GET /api/v1/search?q=&kind=&team=` | Search |
| `GET /api/v1/messages/{type}` | Look up an event type across the catalogue |
| `GET /schemas/{type}/{semver}.json` | Stable dataschema URL (public within the network, no auth) |

## Deployment

- **Image:** multi-stage build to a distroless static image, built with `podman build`. It's a single static binary (`CGO_ENABLED=0`).
- **Helm chart (`deploy/helm/api-portal`):** Deployment (2 replicas, HPA optional), Service, Ingress, ConfigMap (config + rulesets), Secret refs (DB DSN, OIDC client secret), PodDisruptionBudget, and a NetworkPolicy example.
- **Postgres:** external: managed, or CloudNativePG in-cluster. The chart doesn't bundle a database.
- **The portal's own observability:** `slog` JSON logs; OpenTelemetry traces and metrics over OTLP (optional); `/healthz` and `/readyz`; Prometheus `/metrics`.
- **Backups:** Postgres only, since all state lives there.

## Local development (Taskfile)

| Task | Does |
|---|---|
| `task dev:db` | Postgres via `podman-compose` |
| `task run` | `portal serve` against the dev DB with a stub OIDC provider |
| `task test` | Unit + golden-file tests for the pipeline packages |
| `task test:integration` | Tests against a disposable Postgres container |
| `task check:examples` | `portal check` over `docs/spec/examples/*` |
| `task image` | `podman build` |

## Dependencies

Licences must be verified at implementation time.

| Purpose | Library | Licence |
|---|---|---|
| OpenAPI parse / model | `github.com/pb33f/libopenapi` | MIT |
| Lint engine (OAS, AsyncAPI) | `github.com/daveshanley/vacuum` (motor package) | MIT |
| OpenAPI diff / breaking | `github.com/oasdiff/oasdiff` | Apache-2.0 |
| JSON Schema validation | `github.com/santhosh-tekuri/jsonschema/v6` | Apache-2.0 |
| YAML | `go.yaml.in/yaml/v3` (keeps node positions for line numbers) | MIT / Apache-2.0 |
| OIDC | `github.com/coreos/go-oidc/v3` | Apache-2.0 |
| Postgres | `github.com/jackc/pgx/v5` | MIT |
| Migrations | `github.com/pressly/goose/v3` | MIT |
| UI | htmx (0BSD), Scalar API reference (MIT) | — |

**AsyncAPI v3 in Go has no mature library.** We decode it into our own structs and validate against the official AsyncAPI JSON Schema (the `asyncapi/spec-json-schemas` package). The risk is contained because AsyncAPI rendering and diff are phase 2. vacuum v0.30.6 lints AsyncAPI v3, but one built-in rule raised an internal error on a minimal document, so pin the version and track it upstream.
