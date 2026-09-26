# 04 — Governance: lint, diff, versioning

Governance runs in two places with **identical logic**, from the same Go packages:

| Where | Command | Baseline for diff | Effect |
|---|---|---|---|
| PR / local | `portal check` | Fetched from the portal (read-only token), or `--baseline <file>` for fully offline runs | Exit code + report (text, JSON, SARIF, JUnit) |
| Main / tag | `portal push` | The server's stored versions | Publish or reject; the report is stored |

The server re-runs everything on push and never trusts the client's result.

## Outcomes

Each check produces findings with a severity:

| Severity | `check` | `push` |
|---|---|---|
| `error` | exit 1 | rejected |
| `warn` | exit 0 (exit 1 with `--strict`) | published, and counted in the score |
| `info` | shown | published |

### Acknowledging a finding
Sometimes a breaking change is intentional and safe, for example when a field was never populated. Any `error` finding can be acknowledged:

```
portal push --ack BRK-OAS-0042 --ack-reason "field never populated, verified in logs"
```

An acknowledgement is recorded on the version and in the audit log with the actor and the reason. Change ids are stable for identical input, so the same id can go into the CI config for a single run. Acks don't carry over to later versions.

---

## 1. Lint

### Rulesets
Rulesets are **vacuum / Spectral-format YAML** files in the portal config, so the Spectral ecosystem can be reused. Each spec kind has a default ruleset, and an API can pick another with `ruleset:` in its descriptor, but only from rulesets an admin has configured. There are no rulesets carried in repos, which keeps standards central.

| Ruleset | Contents |
|---|---|
| `openapi-default` | vacuum `recommended` without `oas3-missing-example` and `component-description` + org rules (below) + the curated security subset |
| `asyncapi-default` | vacuum AsyncAPI rules + org rules on the normalised model |
| `cloudevents-default` | Portal-native Go rules (below). vacuum doesn't know this format. |

**The OWASP rules are curated, not enabled wholesale.** Measured on our [example spec](examples/orders-service/api/openapi.yaml) with vacuum v0.30.6: `recommended` scores 96/100 with 0 errors by vacuum's own scoring, while hard mode (every OWASP rule) scores 10/100 with 14 errors, mostly rate-limit headers on every response and mandatory 429/500 responses. Turning on everything would train teams to ignore the linter. By the same reasoning, `openapi-default` leaves out the two `recommended` rules that made 12 of its 13 warnings on the example (missing examples and missing component descriptions). The portal's score for the example (100 − 10 per error − 2 per warning) is 94. The security subset starts as:

| Rule | Severity |
|---|---|
| Every operation has `security` (or an explicit `security: []` with an `x-portal-public: true` justification) | error |
| No credentials in query parameters (`apiKey in: query`) | error |
| No HTTP basic auth | error |
| No `http://` server URLs, except `localhost` | error |
| String properties have `maxLength` / `pattern` / `enum` / `format` (OWASP string-restricted) | warn |
| Integers have `format` and bounds | warn |
| 401/403 responses defined on secured operations | warn |
| Rate-limit headers, 429 / 500 responses | off by default; per-API opt-in |

### Org rules — OpenAPI
| Rule id | Severity | Rule |
|---|---|---|
| `org-operation-id` | error | Every operation has a unique `operationId` in camelCase. |
| `org-owner-contact` | warn | `info.contact` present (the portal can fill it from the owner team). |
| `org-problem-json` | warn | 4xx/5xx responses use `application/problem+json`. |
| `org-no-version-in-path` | warn | No `/v1/` in paths: the version belongs in the spec and in routing, not in URLs. |
| `org-servers-match-env` | warn | `servers[].url` values appear in the descriptor's `environments`. |

### Portal-native rules — event catalogue (and normalised AsyncAPI)
| Rule id | Severity | Rule |
|---|---|---|
| `ce-type-format` | error | `type` is reverse-DNS, lower-case, and ends in `.vN`. |
| `ce-type-prefix` | error | `type` starts with an org-configured prefix (e.g. `com.acme.`). |
| `ce-type-unique` | error | The `type` isn't owned by another API (invariant 1 in the model). |
| `ce-dataschema-resolves` | error | `dataschema` resolves, and it is a valid JSON Schema of a supported draft. |
| `ce-examples-valid` | error | Every example `data` validates against `dataschema`. |
| `ce-binding-present` | error | At least one binding after `defaults` are merged. |
| `ce-kafka-key` | warn | Kafka bindings declare a `key`: ordering depends on it. |
| `ce-type-major-matches-schema` | warn | The `.vN` in the type equals the payload schema's declared major, if the schema has one. |
| `ce-topic-single-owner` | warn | A topic, subject or queue isn't produced to by more than one owner team. |
| `ce-description` | warn | `description` is present on every `produces` message. |
| `ce-json-only` | error (MVP) | `datacontenttype` is JSON (`application/json` or `+json`). Other formats come in P3. |

### Score
`score = 100 − Σ(weight × findings)`, floored at 0, where the weights are error 10, warn 2 and info 0. The score is shown per version and aggregated per team. Its only purpose is visibility: gating uses severities, never the score.

### Changing a ruleset
When a ruleset changes, its **version hash** changes. The portal recomputes lint for the latest version of every affected API in the background and updates the scores. Published versions are never un-published. New pushes get the new rules. Admins can mark a new `error` rule as `warn-until: YYYY-MM-DD` to give teams a grace period.

---

## 2. Diff and breaking changes

### Baseline selection
For a pushed version *V*, the baseline is the **highest published, non-pre-release version lower than V**.

- Pushing `2.4.0` when `2.3.1` exists compares against `2.3.1`.
- Pushing `1.9.0` when `2.0.0` exists compares against `1.8.x`. Older major lines can still be maintained.
- Pushing `1.8.0` when `1.9.0` exists is rejected: versions must increase within a major line.
- The first version has no baseline, so there's no diff.

### OpenAPI
Use **oasdiff** (Go library). Its breaking-change checks are the source of truth, and its levels map as `ERR` → `error` and `WARN` → `warn`, with `INFO` counted as additive. Two of its defaults are overridden to match this table: removing an optional response field is breaking (oasdiff says `INFO`), and a new response enum value is a warning (oasdiff says `ERR`). oasdiff's own version checks are off, because the semver gate below owns versioning. Examples:

| Change | Breaking? |
|---|---|
| Remove an operation / path | yes |
| Add a required request field or parameter | yes |
| Remove a response field, or make a required response field optional | yes |
| Change a field's type; narrow an enum on a request / widen one on a response | yes |
| Add an optional request field, a new operation, or a new response field | no |
| Add a new enum value to a response | warn: clients with exhaustive switches break |

### Event schema compatibility
JSON Schema has no built-in compatibility model, so we define one, borrowing Kafka Schema Registry's vocabulary. Let *old* and *new* be the payload schemas of the same `type`:

| Mode | Meaning | Guarantees |
|---|---|---|
| `FORWARD` | Every event valid under *new* is valid under *old* | Consumers still on the old schema can read new events. |
| `BACKWARD` | Every event valid under *old* is valid under *new* | The owner (the reader) still accepts events from senders on the old schema. |
| `FULL` | Both | Either side can upgrade first. |
| `NONE` | No check | Experimental only. |

**The default depends on the message's role:**
- `role: produces` → **FORWARD**. The owner writes and other teams read.
- `role: receives` → **BACKWARD**. Other teams write and the owner reads, as with commands.

The descriptor's `compatibility` overrides this default for every message in the API.

The checker is **structural** and works over a supported subset of keywords: `type`, `properties`, `required`, `additionalProperties`, `enum`, `const`, `items`, `minimum`/`maximum`, `minLength`/`maxLength`, `pattern`, `format`, and local `$ref`:

| Change to the payload schema | FORWARD (produces) | BACKWARD (receives) |
|---|---|---|
| Add an optional property | ok* | ok* |
| Add a required property | ok* | **breaking** |
| Remove a property from `required` | **breaking** | ok |
| Remove a property | **breaking** if it was required | ok* |
| Widen an enum | **breaking** | ok |
| Narrow an enum | ok | **breaking** |
| Change a type | **breaking** | **breaking** |
| Tighten a constraint (e.g. lower `maxLength`) | ok | **breaking** |
| Loosen a constraint | **breaking** | ok |

\* This assumes the other side's schema allows additional properties. If it has `additionalProperties: false`, adding a property is breaking.

**Any change involving keywords outside the subset** (`oneOf`, `anyOf`, `if`/`then`, `patternProperties`, `dependentSchemas`, …) is reported as `BRK-UNVERIFIED` with severity `error`. The team can bump the major version or acknowledge the finding. Being conservative is deliberate: a missed breaking event change is the most expensive failure this portal can let through.

### Event contract changes beyond the payload
| Change | Breaking for `produces`? |
|---|---|
| Remove a message type | yes |
| Add a message type | no |
| Change or remove a binding (topic, subject or queue) | yes |
| Add a binding | no |
| Change the Kafka `key` or `mode` | yes: breaks ordering or deserialisation |
| Change `datacontenttype` | yes |
| Add a required extension | no (produces); yes (receives) |
| Remove a required extension | yes (produces); no (receives) |
| Change `source` / `subject` pattern | warn |

**Recommended pattern for breaking payload changes:** introduce `…order.created.v2` alongside `.v1`, which is additive and needs only a minor bump. Deprecate `.v1`, and remove it later in a major bump. The docs and the `ce-type-major-matches-schema` rule both steer teams toward this.

---

## 3. Versioning and release policy

| Rule | Enforced as |
|---|---|
| The version comes from the spec (`info.version` / `version`) and must be valid semver. | error |
| The same version with different content is rejected, and with identical content it's a no-op. | error / no-op |
| Versions must increase within a major line (see baseline selection). | error |
| A breaking change against the baseline requires a higher major (or minor while `0.x`). | error, unless acked |
| Additive changes should bump minor; doc-only changes should bump patch. | warn |
| Pre-releases (`-rc.1`) skip the major-bump gate but are hidden from default views and never become a baseline. | behaviour |

### Lifecycle
```
experimental ──► production ──► deprecated ──► retired
      ▲               │ ▲            │
      └───────────────┘ └────────────┘   (reversals allowed; they produce a warning)
```

| State | Effect |
|---|---|
| `experimental` | Breaking-change findings are downgraded to `warn`, and an "unstable" badge is shown. |
| `production` | Full gating. |
| `deprecated` | Banner shown, with `sunset` if set. The API ranks lower in search. Declared consumers appear on a report. Pushes are still accepted for fixes. |
| `retired` | Read-only and hidden from search, but reachable by URL. Pushes are rejected. Its event types are released for reuse after 90 days. |

A **release** in the phase-2 sense is a published version plus a generated changelog: the DiffReport rendered as Markdown, with an optional human-written note from a `CHANGELOG.md` section that matches the version.
