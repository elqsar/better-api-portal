# Onboarding (M4)

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
