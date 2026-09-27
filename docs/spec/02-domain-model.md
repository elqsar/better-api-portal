# 02 — Domain model

All three input formats are parsed into one **normalised model**. The UI, search, diff, lint scoring and the future MCP server read this model, so none of them need to know whether an API came from OpenAPI, AsyncAPI or an event catalogue. The original upload is always stored unchanged alongside the model, and it can be served back byte for byte.

## Entities

```
Team 1───* API 1───* Version 1───1 SpecBundle (raw files, content-addressed)
                       │
                       ├───* Operation   (HTTP: method + path)
                       ├───* Message     (event: CloudEvents type / AsyncAPI message)
                       │       └───* Binding (kafka topic, nats subject, …)
                       ├───* Schema      (named, addressable; shared by ops & messages)
                       ├───1 LintReport  ──* Finding
                       └───0..1 DiffReport (vs previous version) ──* Change

API *───* API   via Dependency (from descriptor `consumes`)
Repo 1───* API  (claim: which repo may push to which API id)
```

### Team
`slug`, `name`, `contact` (links), and `members`, which are resolved from OIDC groups (see [05-architecture](05-architecture.md#auth)). Teams come from config and are not created by pushes.

### API
| Field | Notes |
|---|---|
| `id` | From the descriptor. Immutable, and the primary key. |
| `kind` | `openapi` \| `asyncapi` \| `cloudevents` |
| `style` | Derived: `sync` for openapi, `async` for the other two. |
| `title`, `description` | From the latest version's spec, overridable in the descriptor. |
| `owner` | Team slug. |
| `system`, `tags`, `links` | From the descriptor. |
| `lifecycle`, `sunset` | From the descriptor, taken from the latest push. |
| `compatibility` | For async only. |
| `repo` | Claimed repo URL. |
| `latest_version` | Pointer to the highest semver version that is **not** a pre-release. |

Descriptor-level fields such as owner, lifecycle and tags belong to the **API**, not the version. Each push updates them, so an owner can deprecate an API without cutting a new spec version. The history of these changes is kept in the audit log.

### Version
| Field | Notes |
|---|---|
| `api_id` + `semver` | Unique. **Immutable:** pushing the same semver with different content is rejected. Pushing it with identical content is a no-op, which keeps CI retries safe. |
| `content_hash` | sha256 of the canonicalised bundle. |
| `source` | `{ repo, commit_sha, ref, ci_run_url, pushed_by, pushed_at }` |
| `status` | `published` \| `rejected`. Rejected pushes are kept for 30 days so the owner can see why. |
| `prerelease` | `true` if the semver has a pre-release tag (`2.0.0-rc.1`). Pre-releases are hidden from default views. |

### Operation (sync)
`method`, `path`, `operation_id`, `summary`, `description`, `tags`, `deprecated`, `security` (scheme names), `parameters[]`, `request` (media type → schema ref), and `responses` (status → media type → schema ref).

### Message (async)
| Field | From the event catalogue | From AsyncAPI v3 |
|---|---|---|
| `key` | CE `type` | `messages.<name>`, or its CE `type` if a CloudEvents trait declares one |
| `role` | `role` | `operations[].action`: `send` → `produces`, `receive` → `receives` |
| `summary`, `description` | same | same |
| `ce` | `{source, subject, datacontenttype, dataschemauri, extensions}` | headers schema, if it follows the CE naming convention (`ce_*` / `ce-*`) |
| `payload` | schema ref from `dataschema` | `payload` |
| `bindings[]` | `bindings` (after merging `defaults`) | `channels[].address` + channel/operation bindings |
| `examples[]` | `examples` | `examples` |

### Binding
`protocol` (`kafka`, `nats`, `sns`, `sqs`, `eventbridge`, `pubsub`, `servicebus`), `address` (the topic, subject, queue or bus name), and `props` (protocol-specific fields such as key and mode). Bindings are indexed, which answers the question "what flows on topic `orders.events`?" across all APIs.

### Schema
Every named or referenced schema gets a stable **pointer** inside its version, e.g. `#/components/schemas/Order` or `schemas/order-created.v1.json`. It also gets a flattened **field index** (`path`, `type`, `required`, `description`) that feeds search and the structured diff. Schemas are not deduplicated across APIs in MVP.

### Dependency
`from_api` → `to_api`, with optional `types[]`. It comes from the descriptor's `consumes`, which is declared per repo, so the dependency is attached to **every API in that repo**. A dependency on an unknown API id is kept as *dangling* and resolves when that API appears. Because it is declared per repo, the UI lists consumers by **service (repo)**, with its APIs underneath, not as separate consumers.

Combining messages with `role: produces` and dependencies gives the **event flow graph** (phase 2): for each event type, its producer and its declared consumers.

### LintReport / Finding
`ruleset` (name + version hash), `score` (0–100), and `findings[]` with `{rule_id, severity, message, pointer, line}`. A report is computed on push. It is recomputed in the background when a ruleset changes, which updates the score but not the gating (see [04-governance](04-governance.md)).

### DiffReport / Change
The comparison runs against the previous **published, non-pre-release** version. Each change has `{kind: added|removed|changed, target: operation|message|schema-field|binding|metadata, pointer, breaking: bool, rule_id, message}`. On-demand diffs between any two versions are computed and cached, not stored.

## Identity and URLs

| Resource | URL |
|---|---|
| API | `/apis/{id}` |
| Version | `/apis/{id}/versions/{semver}` (or `latest`) |
| Operation | `/apis/{id}/versions/{v}/operations/{operation_id or METHOD+path}` |
| Message | `/apis/{id}/versions/{v}/messages/{type}` |
| Raw spec | `/apis/{id}/versions/{v}/raw/{path-in-bundle}` |
| Event schema (stable) | `/schemas/{type}/{semver}.json`: what `dataschema` points to when a team hasn't set its own URI |
| Diff | `/apis/{id}/diff/{from}...{to}` |

These URLs are part of the contract, because CI logs, chat links and CE `dataschema` attributes will point at them. Treat changing them as a breaking change of the portal.

## Invariants

1. A CloudEvents `type` is produced (or received) by **at most one API** at any time. A second API that claims it is rejected on push, which makes ownership of every event unambiguous.
2. A Kafka topic, NATS subject and so on may carry types from several APIs. No uniqueness is enforced on bindings, but a lint rule warns when one topic mixes owners.
3. Versions are append-only. The only deletion is an admin action, and it is audited.
4. Renaming an API id doesn't exist. Use a new id and deprecate the old one.
