# API Portal — Specification

Status: **Draft v0.1** · Last updated: 2026-09-25

An internal developer portal for discovering, documenting and governing the
company's APIs — both synchronous (HTTP/OpenAPI) and asynchronous (events on
Kafka, NATS and cloud buses). It is built internally first, with open-sourcing
as a later option, so nothing company-specific is hard-coded: org names, teams,
environments and rulesets are configuration.

## Problem

- Nobody can answer "which APIs exist, who owns them, and what do they look
  like right now?" without asking around.
- Specs live in service repos but are not published anywhere discoverable.
- Breaking changes reach consumers because nothing checks a spec against its
  previous version before release.
- Events are the least documented surface: CloudEvents `type`s and JSON Schemas
  exist, but there is no catalogue of who produces and who consumes what, or
  on which topic/subject.

## Goals

1. **One catalogue** of every API — sync and async — with owner, lifecycle and
   version history.
2. **Design-first governance in CI**: lint, security rules and breaking-change
   detection run on every PR and block releases that violate policy.
3. **First-class events**: CloudEvents + JSON Schema are catalogued as well as
   OpenAPI, including broker bindings and producer/consumer relationships.
4. **Low operational cost**: a single Go binary plus Postgres, deployable with
   Helm. No platform team required to keep it alive.

## Non-goals (for now)

- Runtime observability / tracing (see [roadmap](06-roadmap.md), phase 3).
- API gateway, traffic management or rate limiting.
- Code-first spec generation (phase 3).
- A general software catalogue (services, libraries, infra) — that is
  Backstage's job; we stay API-only and interoperate with it.
- Public/external developer portal features (sign-up, API keys, billing).

## Initial sizing

~50 APIs, ~20 teams, a few hundred engineers as readers. Design targets are
10× that (500 APIs, ~5 000 versions) without architectural change.

## Personas

| Persona | Wants |
|---|---|
| **API consumer** (any engineer) | Find an API or event, read its docs, see examples, know whom to ask. |
| **API owner** (team building a service) | Publish specs from CI with no manual steps; learn about breaking changes before merge. |
| **Platform / architecture** | Consistent standards across teams; visibility of quality and deprecated APIs. |
| **Portal admin** | Configure SSO, teams, rulesets; keep it running. |
| **AI coding agent** (phase 2) | Query the catalogue programmatically to pick the right API and schema. |

## Glossary

| Term | Meaning |
|---|---|
| **API** | A named, owned contract. One of: an OpenAPI document, an AsyncAPI document, or a CloudEvents catalogue. |
| **Version** | An immutable, semver-tagged snapshot of one API's spec. |
| **Descriptor** | `portal.yaml` in a service repo, listing the APIs that repo publishes. |
| **Event catalogue** | Our YAML format describing CloudEvents types, schemas and bindings (`events.yaml`). |
| **Binding** | Where a message is carried: a Kafka topic, NATS subject, SNS topic, etc. |
| **Ruleset** | A named set of lint rules applied to a spec kind. |
| **Push** | CI uploading a new version via `portal push`. |

## Reading order

1. [01-product.md](01-product.md) — features, journeys, MVP scope
2. [03-formats.md](03-formats.md) — the files teams write (read before the model)
3. [02-domain-model.md](02-domain-model.md) — how everything is normalised
4. [04-governance.md](04-governance.md) — lint, diff, versioning policy
5. [05-architecture.md](05-architecture.md) — components, storage, auth, deploy
6. [06-roadmap.md](06-roadmap.md) — phases, open questions, decisions

Machine-readable pieces: [`schemas/`](schemas/) (JSON Schemas for the two file
formats) and [`examples/`](examples/) (a complete sample service).
