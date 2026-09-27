# Onboard a service

This guide puts a service's APIs in the portal. You add two files, `portal.yaml`
and a CI workflow, and the first push from `main` publishes the APIs. From then
on, every pull request that changes a spec is checked. It should take about half
an hour, most of it spent fixing what the linter finds in specs that were never
linted.

What you need:

- the portal's URL, e.g. `https://api-portal.acme.internal`;
- your team's slug, as listed in the portal's configuration (e.g. `team-orders`);
- specs in the repo: OpenAPI 3.0/3.1 for HTTP, and for events either an
  [event catalogue](../spec/03-formats.md#2-event-catalogue--eventsyaml) or
  just the JSON Schemas of your CloudEvents payloads (see step 2b).

Swagger 2.0 and AsyncAPI 2 aren't accepted. `portal init` names such files and
says how to convert them.

## 1. Install the CLI

Download a release from `github.com/elqsar/better-api-portal/releases`. Each
archive holds a single static binary:

```sh
v=v0.1.0; os=darwin; arch=arm64   # or linux / amd64
curl -fsSLO https://github.com/elqsar/better-api-portal/releases/download/$v/portal_${v}_${os}_${arch}.tar.gz
tar -xzf portal_${v}_${os}_${arch}.tar.gz && mv portal_${v}_${os}_${arch}/portal /usr/local/bin/
portal version
```

You can also run `go install github.com/elqsar/better-api-portal/cmd/portal@v0.1.0`,
or use the container image, which has the same binary as its entrypoint.

## 2. Generate the files

### 2a. The repo has specs

From the repository root:

```sh
portal init --owner team-orders --ci github --portal-url https://api-portal.acme.internal
```

This writes:

- **`portal.yaml`**, with one API per spec found. The only OpenAPI spec is
  named `<service>-http` and the only event catalogue `<service>-events`.
  `<service>` is the directory name without a `-service`, `-svc` or `-api`
  suffix, and `--service` overrides it. If a kind has several specs, each is
  named from its title.
- **`.github/workflows/api-portal.yml`**. On pull requests it runs
  `portal push --dry-run`, which gives the portal's verdict and stores nothing.
  On `main` and tags it runs `portal push`. Findings appear as PR annotations
  through SARIF.

Then it runs `portal check` on the result. Add `--stdout` to see the descriptor
without writing it. Existing files are only replaced with `--force`.

**Review the ids before you commit.** The first push claims an id for this repo
forever. It is the API's URL and the key its versions are compared by. Rename
now if the defaults are wrong.

Fill in the commented fields where they apply: `system`, `links` (runbook,
chat channel), `environments` (URLs for HTTP, broker names for events), and
`consumes` (the other APIs this repo depends on, so their owners can see you).
See [03-formats](../spec/03-formats.md#1-descriptor--portalyaml) for every
field.

### 2b. Existing CloudEvents with JSON Schemas

If your events are described only by payload schemas, `portal init` drafts the
catalogue for you:

```sh
portal init --owner team-orders --ci github --portal-url https://api-portal.acme.internal \
  --events-from api/schemas --type-prefix com.acme.orders. --kafka-topic orders.events
```

- It creates one message per schema in the directory. A schema that other
  schemas `$ref` (a shared `money.json`) is treated as a part, not a message.
- If a schema's `$id` is already a type name, it is kept as the type.
  Otherwise the type is built from the prefix and the file name, so
  `order-created.v1.json` becomes `com.acme.orders.order.created.v1`. The
  major version comes from the file name or `$id`, and defaults to `v1`.
- The catalogue is written next to the directory (`api/events.yaml` here) and
  added to `portal.yaml`.
- It lists every type with where its name came from, and marks the ones that
  break the portal's naming rules (below).

Then edit the draft:

- **`summary` and `description`** of each message: when it is emitted, and
  what consumers can rely on.
- **`role`** of each message. `produces` means you emit it. `receives` means
  other services send it to you, but you own the contract, as with commands.
  Events that another team owns and you consume do **not** belong here; put
  them under `consumes` in `portal.yaml`. Declaring them here claims their
  type, and the push is rejected with `ce-type-unique`.
- **`bindings`**: where the events are carried (Kafka topic and key, NATS
  subject, and so on). Give `--kafka-topic` or `--nats-subject` for a default,
  or add them by hand. A message without a binding is an error.
- **`examples`** (optional): they are validated against the schema.

#### Type names that break the rules

Types must be reverse-DNS, lower-case, and end in a major version
(`com.acme.orders.order.created.v1`, rule `ce-type-format`). They must also
start with the organisation's prefix if the portal sets one (`ce-type-prefix`).

A type that producers already send can't simply be renamed, because consumers
match on it. Publish the correct name as a **new type** alongside the old one,
move consumers over, then mark the old one `deprecated: true` and remove it in
a major version. Until then, keep the old type out of the catalogue: `portal
init` lists it, and nothing else depends on the draft containing it.

## 3. Fix what `portal check` reports

Run it as often as you like. It is offline and changes nothing:

```sh
portal check
```

Errors fail the check and block publishing; warnings don't (unless you pass
`--strict`). Each API also gets a score out of 100. These findings are the
most common ones on specs that have never been linted:

| Rule | Level | Fix |
|---|---|---|
| `descriptor-owner-unknown` | error | The owner must be a team slug in the portal's configuration. Ask the portal admins if yours is missing. |
| `sec-operation-security` | error | Every operation needs `security`. For a deliberately public one, write `security: []` and add `x-portal-public: <why>`. |
| `sec-https-servers` | error | `servers` URLs must use `https`. |
| `org-operation-id` | error | `operationId`s are camelCase: `getOrder`, not `get_order`. |
| `ce-type-format`, `ce-type-prefix` | error | See [type names](#type-names-that-break-the-rules). |
| `ce-binding-present` | error | Give every message a binding, or set `defaults.bindings`. |
| `ce-examples-valid` | error | An example doesn't match its schema. Fix one or the other. |
| `org-owner-contact` | warn | Add `info.contact` to the OpenAPI spec. |
| `sec-auth-responses` | warn | Secured operations should document `401`. |
| `sec-integer-bounds`, `sec-string-restricted` | warn | Give integers `minimum`/`maximum`, and strings `maxLength`, `enum` or `pattern`. |
| `ce-description` | warn | Describe each produced event: when it is emitted, and what consumers can rely on. |
| `ce-kafka-key` | warn | Say what the Kafka key is (`key: { from: data, pointer: /orderId }`), since ordering depends on it. |

The rules and their rationale are in [04-governance](../spec/04-governance.md).

## 4. Commit and publish

Commit `portal.yaml`, the workflow and any spec edits, and open a pull request.

- **On the pull request**, the workflow's check step shows the portal's
  verdict, including rules that only the portal can check: owner teams,
  duplicate event types, and topic ownership. Findings appear as annotations
  on the diff.
- **On merge to `main`**, the first push publishes every API and claims its id
  for this repo. The CI log prints each API's URL in the portal. A rejected
  API prints its findings and fails the job.

The workflow authenticates with the job's GitHub OIDC token, so there's no
secret to set up. It needs `permissions: id-token: write`, which the template
already has.

## Afterwards

### Versions and breaking changes

The version is the spec's own: `info.version` in OpenAPI, and `version` in the
event catalogue. Every push compares the spec with the latest published version:

- **A changed spec needs a new version.** Publishing different content under
  a version that is already published is rejected (`version-immutable`).
- **A breaking change needs a major bump**, such as a removed response field
  or an event payload that consumers can no longer read. Without one, the
  check fails with an id like `BRK-OA-29783f` and names the version required.
- **If you're sure the change is safe**, for example because the field was
  never sent, acknowledge it once instead of bumping:
  `portal push --ack BRK-OA-29783f --ack-reason "never populated"`. The reason
  is shown on the API's page.

What counts as breaking for event payloads depends on the message's role
(04-governance, "Event schema compatibility").

### Descriptor-only changes

Changing only `portal.yaml` (`consumes`, `links`, `environments`, `tags`,
`lifecycle`) needs no version bump. The next push from `main` updates the API,
and the log says `unchanged (metadata updated)`.

### Deprecating

Set `lifecycle: deprecated` and, optionally, `sunset: YYYY-MM-DD`. The API's
pages show a banner, and search ranks it lower.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `401` from the portal in CI | The job can't get an ID token. Check that `permissions: id-token: write` is set. Pull requests from forks never get one, so for those, run `portal check` against a checkout of the base branch instead (`--baseline`). |
| `403` on push | Only `main` and tags may publish. Other refs can check (`--dry-run`) but not push. |
| `api-claimed` | Another repo published this id first. Pick another id, or ask a portal admin to transfer it. |
| `version-immutable` | The spec changed without a version bump. Bump the version. |
| `ce-type-unique` | Another API already declares this event type. If you consume it, move it to `consumes` in `portal.yaml`. |
| `consumes-unknown-api` (warning) | You depend on an API that isn't in the portal yet. It resolves when that API is published. |
| `descriptor-kind-mismatch` | The `kind` in `portal.yaml` doesn't match the file's content. |
| Swagger 2.0 or AsyncAPI 2 file skipped | Convert it: `swagger2openapi` for Swagger, and the [AsyncAPI v3 migration guide](https://www.asyncapi.com/docs/migration/migrating-to-v3) for AsyncAPI. |
| Several services in one repo | Run `portal init --root <dir>` per service. In CI, run one job per descriptor, adding `--descriptor <dir>/portal.yaml` to both portal commands. |
