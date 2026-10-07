# Push and server

## Push pipeline

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

## Server

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
- **Claims** follow [03-formats](../spec/03-formats.md): only a *successful* push claims an id.
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
  and `portal push` prints "(metadata updated)". It also rewrites the
  `api` search document of every published version (`refreshAPIDocs`,
  with `index.APIDoc`), the only one that carries the title and tags, as
  `portal reindex` would; `tsv` is generated, so it follows.
- **The bundle download** carries `X-Portal-Version`,
  `X-Portal-Content-Hash` and `X-Portal-Lifecycle`, so a check can use it as
  a baseline. It needs the same authentication as push, for now.
