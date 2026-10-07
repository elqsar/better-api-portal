# Checker

Decisions for `portal check` (M1) where the spec was silent.

- **`--baseline`** can be repeated. It takes at most one previous
  `portal.yaml`, with APIs matched by id, plus any number of
  `api-id=bundle.tar.zst`, which take precedence. Bundles carry no lifecycle,
  so `lifecycle-reversal` isn't checked against them. They're unpacked into
  a temporary directory that is removed after the run.
- **Content hash** (see [05-architecture](../spec/05-architecture.md), "Bundle format"): sha256 over paths
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
