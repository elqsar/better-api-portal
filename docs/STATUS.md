# Implementation status

Last updated: 2026-09-25 · Last commit: `7800aca`

A handoff note for continuing the work. The spec is in [spec/](spec/README.md),
and the roadmap and milestones are in [spec/06-roadmap.md](spec/06-roadmap.md).

## Where we are

We're partway through milestone **M1: `portal check` (offline)**. Event
catalogues get the full pipeline. OpenAPI is parsed and linted but not
diffed yet.

| # | Commit | What |
|---|---|---|
| 0 | `a54337b` | Specification draft v0.1 |
| 1 | `ac4806e` | `portal check` + `portal.yaml` validation (embedded JSON Schema, line numbers) |
| 2 | `32b82e2` | Event catalogue → normalised model (defaults merge, sandboxed `$ref` loading) |
| 3 | `1c14a7b` | Native CloudEvents lint rules, score, `--config` |
| 4 | `e4e1e3c` | JSON Schema compatibility checker (`internal/compat`), every row of the governance table tested |
| 5 | `952c256` | Event contract diff, `--baseline`, `portal diff`, semver gate, `--ack` |
| 6 | `7800aca` | OpenAPI → model, `openapi-default` lint (vacuum recommended + org + security rules) |

### J2 for events, today
```sh
git worktree add ../base main
portal check --config portal.config.yaml --baseline ../base/portal.yaml
# a breaking change without a major bump → exit 1 with an id such as BRK-CE-811dea
portal check ... --ack BRK-CE-811dea --ack-reason "why it is safe"
portal diff old/events.yaml new/events.yaml   # exit 1 if any change is breaking
```

## Code map

| Package | Role |
|---|---|
| `cmd/portal` | cobra CLI: `check`, `diff` |
| `internal/check` | Orchestrator: descriptor → per-API parse → lint → (baseline) diff → policy |
| `internal/descriptor` | `portal.yaml` load and validation; `Sniff` detects a spec's kind |
| `internal/yamldoc` | YAML with pointer→line index; schema validation; violation flattening |
| `internal/bundle` | `$ref` file closure (remote refs, escapes and missing files are problems); `RelTo` |
| `internal/spec/eventcatalog` | `events.yaml` → `model.Spec`, plus the compiled payload schemas |
| `internal/spec/openapi` | OpenAPI 3.0/3.1 → `model.Spec` (built from the raw docs, not libopenapi) |
| `internal/model` | `Spec`, `Message`, `Binding`, `Operation`, `Schema`, `Finding`, `Change` |
| `internal/lint` | `CloudEvents`, `OpenAPI` (vacuum + native), `Score` |
| `internal/compat` | `Check(old, new, mode)`, which reduces to the subset test `sub(A,B)`; `Fingerprint` |
| `internal/diff` | `Events` contract diff, `SameContent` |
| `internal/policy` | Semver gate, lifecycle, pre-releases, acks |
| `internal/report` | Text and JSON output |
| `internal/config` | Minimal `portal.config.yaml` (org prefix, teams) |

Tasks: `task build | test | vet | check:examples` (Taskfile, not Make).
Golden files are regenerated with `go test ./<pkg> -update` (eventcatalog,
openapi, compat).

## Decisions taken where the spec was silent

- **`--baseline`** takes the *previous version's `portal.yaml`*. APIs are
  matched by id. Bundles will be accepted later.
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
- **Native OpenAPI schema rules** only cover the entry file. vacuum covers
  external files.
- **vacuum stays** (decided 2026-09-26). It costs a lot: the binary grows
  from 6.2 MB to 69.3 MB, and the module graph from 14 to 145 modules. We
  accept that because D9 needs Spectral-format rulesets and the M2 server
  lints too. `openapi-default` drops `oas3-missing-example` and
  `component-description`, which made 12 of the 13 warnings on the example.
  orders-http now scores 94, up from 70. The spec's "96/100" was vacuum's own
  score.

## Next steps

1. **OpenAPI diff** with oasdiff: `diff.OpenAPI`, `--baseline` / `portal diff`
   for OpenAPI. Reuse `internal/policy` unchanged, and remove the
   `diff-unsupported` info.
2. **Bundling + content hash** (`internal/bundle`): canonicalise, sha256 and
   tar.zst. This replaces `diff.SameContent` and lets `--baseline` accept a
   bundle.
3. **SARIF / JUnit output** (J2 acceptance: PR annotations).
4. Run `portal check` over real company specs (Q7: `oneOf` usage, Q6: type
   prefix) and tune the rulesets.
5. Then **M2**: Postgres store, `POST /api/v1/push`, CI OIDC, claims, audit.

## Known gaps

- AsyncAPI v3 isn't parsed (the kind is recognised and then skipped).
- Rules that need the server aren't implemented: `ce-type-unique`,
  `ce-topic-single-owner`, owner-team existence, and unknown `consumes`
  ids.
- There are no rulesets from files, no severity overrides and no
  `warn-until` (M5).
- `compat` on OpenAPI component schemas: fragment-only `$ref`s inside a
  component would resolve against the component rather than the file.
  That's irrelevant until OpenAPI schemas go through `compat`; oasdiff is
  planned for OpenAPI instead.
