# 03 — File formats

Teams write three kinds of files, all in their service repo:

| File | Purpose | Defined by |
|---|---|---|
| `portal.yaml` | Descriptor: which APIs this repo publishes, owner, lifecycle, dependencies | **Us** — [schema](schemas/portal.schema.json) |
| `openapi.yaml` (any name) | HTTP API contract | OpenAPI 3.0 / 3.1 |
| `events.yaml` (any name) | Event contract: CloudEvents types, payload schemas, bindings | **Us** — [schema](schemas/eventcatalog.schema.json) |
| `asyncapi.yaml` (any name) | Alternative event contract | AsyncAPI 3.0 |

A full working example lives in [`examples/orders-service/`](examples/orders-service/).

---

## 1. Descriptor — `portal.yaml`

One per repo, at the repo root (the CLI's `--descriptor` flag overrides the path).

```yaml
apiVersion: portal/v1
owner: team-orders                 # default owner for every API below
system: commerce                   # optional grouping shown in the catalogue
links:
  - { title: Runbook, url: https://wiki.internal/orders/runbook }
  - { title: "#team-orders", url: https://chat.internal/channels/team-orders }

apis:
  - id: orders-http
    kind: openapi
    spec: api/openapi.yaml
    lifecycle: production
    tags: [orders, checkout]
    environments:
      - { name: staging, url: https://orders.staging.internal }
      - { name: prod,    url: https://orders.prod.internal }

  - id: orders-events
    kind: cloudevents
    spec: api/events.yaml
    lifecycle: production

consumes:                          # what this repo depends on (optional)
  - api: payments-events
    types: [com.acme.payments.payment.captured.v1]
  - api: customers-http
```

### Rules

| Field | Rule |
|---|---|
| `apiVersion` | Required, `portal/v1`. Lets the format evolve. |
| `owner` | Team slug. It must exist in the portal's team config. It can be overridden per API. |
| `apis[].id` | Globally unique, `^[a-z][a-z0-9-]{2,62}$`, and stable forever: it is the URL and the diff key. The first successful push **claims** the id for that repo, and pushes to it from another repo are rejected until an admin transfers it. |
| `apis[].kind` | `openapi`, `asyncapi` or `cloudevents`. The CLI checks it against the file's content. |
| `apis[].spec` | Path relative to the descriptor. Local `$ref`s from this file are bundled on push. |
| `lifecycle` | `experimental`, `production`, `deprecated` or `retired`. `deprecated` may carry `sunset: YYYY-MM-DD`. |
| `compatibility` | For `cloudevents` and `asyncapi` only: `FORWARD`, `BACKWARD`, `FULL` or `NONE`. If omitted, the default is per message: `FORWARD` for `produces`, `BACKWARD` for `receives`. See [04-governance](04-governance.md#event-schema-compatibility). |
| `environments` | For HTTP: `name` + `url`. For async, broker clusters are configured by an admin in the portal config and referenced by name, e.g. `{ name: prod, broker: kafka-prod }`. |
| `consumes` | Declares dependencies. `types` narrows the dependency to specific event types. Unknown API ids produce a **warning**, not an error, so repos can onboard in any order. |

**The version is not stored in the descriptor.** It comes from the spec itself (`info.version` in OpenAPI/AsyncAPI, `version` in the event catalogue), so there's only one place to bump it.

**Deliberate similarity to Backstage:** `owner`, `system`, `lifecycle`, and API `kind`/`spec` map one-to-one onto Backstage `kind: API` entities. A P3 importer/exporter depends on this.

---

## 2. Event catalogue — `events.yaml`

### Why a new format
CloudEvents standardises the **envelope** (`id`, `source`, `type`, `subject`, `time`, `datacontenttype`, `dataschema`, extensions) and how it is carried by Kafka, NATS, AMQP, HTTP and so on. It does not describe a **catalogue**: which types exist, what their payload is, and where they flow. The CNCF answer to that is **xRegistry**, the CloudEvents registry spec with message definitions, schema registry and endpoints. It's comprehensive, but too verbose for teams to write by hand.

So we define a small hand-written YAML that:
- uses CloudEvents attribute names verbatim,
- references ordinary JSON Schema files (which teams already have),
- can be **exported losslessly to xRegistry** (phase 3). Anything we can't map is rejected at design time, not discovered later.

### Shape

```yaml
eventcatalog: "1.0"                # format version
title: Order events
version: 1.4.0                     # semver of this contract
description: Lifecycle events emitted by the orders service.

defaults:                          # merged into every message unless overridden
  source: /orders-service/{region}
  datacontenttype: application/json
  bindings:
    - kafka: { topic: orders.events, key: { from: data, pointer: /orderId }, mode: binary }

messages:
  - type: com.acme.orders.order.created.v1
    role: produces
    summary: An order was placed.
    description: |
      Emitted once, after the order is persisted and payment is authorised.
    subject: orders/{orderId}
    dataschema: { $ref: ./schemas/order-created.v1.json }
    extensions:
      partitionkey: { required: true, description: Equal to orderId. }
    examples:
      - name: minimal
        data: { orderId: "ord_123", customerId: "cus_9", total: { amount: 1999, currency: EUR } }

  - type: com.acme.orders.order.cancel.requested.v1
    role: receives                 # we own this contract; other services send it to us
    summary: Command to cancel an order.
    dataschema: { $ref: ./schemas/order-cancel-requested.v1.json }
    bindings:
      - nats: { subject: "orders.commands.cancel", stream: ORDERS_CMD }
```

### Message fields

| Field | Req. | Meaning |
|---|---|---|
| `type` | ✔ | CloudEvents `type`. Reverse-DNS, lower-case, dot-separated, ends in a major version `.vN`. Unique across the whole portal. |
| `role` | ✔ | `produces`: this service emits it and owns the contract. `receives`: this service consumes it **and owns the contract**, as with commands. Dependencies on other teams' events go in the descriptor's `consumes`, not here. |
| `summary` / `description` | ✔ / – | Markdown allowed in `description`. |
| `source` | – | A CE `source` pattern. `{var}` segments are placeholders shown in the docs. |
| `subject` | – | A CE `subject` pattern. |
| `datacontenttype` | – | Defaults to `application/json`. MVP payload validation supports JSON only. |
| `dataschema` | ✔ | `{ $ref: <relative file> }` or an inline JSON Schema (`{ schema: {...} }`). Supported drafts: 07, 2019-09 and 2020-12. `$schema` in the file selects the draft, and 2020-12 is the default. |
| `dataschemauri` | – | The URI that producers put in the CE `dataschema` attribute. If omitted, the portal serves a stable URL: `/schemas/{type}/{version}.json`. |
| `extensions` | – | CE extension attributes: `{ name: { required, description, type } }`. |
| `bindings` | ✔ (own or from `defaults`) | One or more carriers (table below). |
| `examples` | – | `data` is validated against `dataschema` by `portal check`. |
| `deprecated` | – | `true` marks this single type as deprecated. |

### Bindings

| Binding | Fields | Notes |
|---|---|---|
| `kafka` | `topic`, `key` (`{from: data, pointer}` / `{from: extension, name}` / `{from: subject}`), `mode` (`binary` \| `structured`) | Follows the CloudEvents Kafka protocol binding. |
| `nats` | `subject`, `stream` (JetStream, optional) | |
| `sns` | `topic` | Topic name, not ARN. The account/region comes from the portal's broker config. |
| `sqs` | `queue` | |
| `eventbridge` | `bus`, `detailType` (optional) | |
| `pubsub` | `topic` | GCP Pub/Sub. |
| `servicebus` | `topic` or `queue` | Azure. |

`defaults.bindings` are **replaced**, not merged, when a message declares its own `bindings`.

### `$ref` resolution (all formats)
- Relative file refs inside the repo are allowed. `portal push` bundles the entry file plus every file reachable through `$ref` into one upload, and the server stores the bundle as is.
- Remote (`http(s)://`) refs are rejected in MVP. The one exception is refs to this portal's own `/schemas/...` URLs, which lets teams share a common `Money` schema across APIs.
- Cyclic refs are allowed (JSON Schema recursion). Refs that escape the repo root (`../../..`) are rejected.

---

## 3. Support matrix

| Format | Ingest & store | Lint | Diff / breaking | Rendered docs |
|---|---|---|---|---|
| OpenAPI 3.1 | MVP | MVP | MVP | MVP |
| OpenAPI 3.0 | MVP | MVP | MVP | MVP |
| Swagger 2.0 | Rejected, with a conversion hint | – | – | – |
| Event catalogue 1.0 | MVP | MVP | MVP | MVP |
| AsyncAPI 3.0 | MVP | MVP | P2 | P2 (raw + message list in MVP) |
| AsyncAPI 2.x | Rejected, with an upgrade hint | – | – | – |
| Avro / Protobuf payloads | P3 | – | P3 | P3 |

## 4. Why not just require AsyncAPI?

The decision is recorded in [06-roadmap](06-roadmap.md#decisions).

Teams already have CloudEvents + JSON Schema. The event catalogue is about 20 lines on top of what exists, while AsyncAPI v3 means learning channels, operations, messages and bindings, with CloudEvents headers modelled by hand as message traits. We accept AsyncAPI for teams that want it and normalise both into the same model ([02-domain-model](02-domain-model.md)), so the UI and governance treat them the same.
