# Web UI (M3)

Agreed 2026-09-26, before M3 started. The spec's
[05-architecture](../spec/05-architecture.md) was updated to match.

- **Stack as in D10:** `html/template` + htmx, `embed.FS`, one hand-written
  CSS file (dark mode via `prefers-color-scheme`), no Node. Every page works
  without JS; htmx only swaps tabs, filters and search results. Strict CSP
  (`script-src 'self'`, no `hx-on`).
- **Model storage:** `version_models.model jsonb` (the serialised
  `model.Spec`, already JSON-tagged) for rendering, plus thin relational rows
  only for queries across APIs: `messages(type, role, api_id, version_id)`,
  `operations`, `bindings(protocol, address, …)`, `dependencies`, and
  `search_docs` (tsvector + `pg_trgm`). No `schemas`/`schema_fields` tables.
  Written in `store.Record`'s transaction; `portal reindex` rebuilds all of
  it from the bundles.
- **Scalar** gets one server-bundled document,
  `/apis/{id}/v/{v}/openapi.json` (external `$ref`s resolved with
  kin-openapi), cached by content hash. Scalar's standalone JS is embedded
  and pinned.
- **Raw diff is server-side** (pure-Go Myers diff, e.g. `hexops/gotextdiff`),
  rendered as an HTML table: per-file YAML by default, canonical JSON as a
  "semantic" toggle, unchanged regions folded and expanded with htmx.
  Cacheable forever, since both sides are content hashes. Replaces the
  spec's "small embedded JS diff library".
- **Pages:** `/`, `/search`, `/apis`, `/apis/{id}/versions/{semver}` (tabs:
  overview, docs, versions, lint, dependencies; deprecation banner),
  `/events/{ce-type}` (across APIs), `/apis/{id}/diff?from=&to=`,
  `/teams/{slug}`. `/admin` after M3.
- **Auth:** OIDC code flow + PKCE, sessions in Postgres, roles from group
  mapping; `/api/v1` reads also accept the session cookie.
- **Index (step 1, done):**
  - Only published versions are indexed. Rejected ones stay reachable
    through `versions` and the reports.
  - `latest_version_id` is the highest release, else the highest
    pre-release (the push baseline stays release-only).
  - `dependencies` are per API (its current `consumes` from `apis.meta`),
    not per version, and may point at APIs not in the portal.
  - Search documents: one per API, operation, message and schema.
    `terms` (identifiers split by `index.Words`, weight A) and `body`
    (summaries, descriptions, schema property names, titles and enum
    values, weight B, capped at 32 KB) both use the `english` config, so
    "refunds" stems to "refund". Schema examples and defaults aren't
    indexed.
  - `check.ParseBundle` re-parses a stored bundle into exactly the model
    the push stored (tested), which is what `portal reindex` uses.
  - Descriptor links, environments and consumes got lower-case JSON tags.
    Rows pushed before that keep `{"API": …}` in `apis.meta` until their
    next push; `reindex` doesn't rewrite `meta`.
  - The page URL is `/apis/{id}/versions/{v}`, as `push` already prints,
    not `/v/`.
- **Web skeleton (step 2, done):**
  - `serve` routes `/api/`, `/healthz`, `/readyz` to the REST API and the
    rest to the UI. Without `oidc.issuer` the UI shows what to configure;
    CI keeps working.
  - Sign-in: code flow + PKCE + nonce; discovery is lazy and retried. One
    `portal_login_<state prefix>` cookie per sign-in in progress (10 min),
    because a browser's `/favicon.ico` request used to start a second
    sign-in and overwrite the first (seen only in a real browser; now a
    test). Only page loads (`Sec-Fetch-Dest: document`, or none) are sent
    to sign in; subresources and htmx requests get 401.
  - Sessions: 32 random bytes in `portal_session` (HttpOnly, SameSite=Lax,
    Secure when publicURL is https), sha256 in `sessions`, 12 h absolute;
    expired rows are deleted at each sign-in. Groups are stored at sign-in,
    so role changes apply at the next sign-in.
  - Non-GET requests with a foreign `Origin` (or `Sec-Fetch-Site:
    cross-site`) get 403.
  - CSP `default-src 'self'`, no inline script or style; htmx configured
    by meta tag with `allowEval: false` and no indicator styles. No
    `hx-boost`: a boosted fetch can't follow a redirect to the identity
    provider.
  - Static files: `/static/<name>?v=<hash>` cached for a year, otherwise
    `no-cache` with an ETag.
  - `--dev-login` stub: issuer `<publicURL>/dev/oidc`, client
    `portal-dev`, offers every configured team group plus the admins
    group; redirects only to the portal's own host and requires S256
    PKCE. publicURL defaults to `http://localhost:<port>` with it.
  - `/api/v1` reads don't accept the session cookie yet; that comes with
    the docs step, which is the first to need it.
- **API list and pages (step 3, done):**
  - URLs: `/apis` (filters `team`, `kind`, `lifecycle`, `tag`, `q`),
    `/apis/{id}` → latest, `/apis/{id}/versions/{v}` (overview; `latest`
    redirects), `…/{v}/lint`, `/apis/{id}/versions` (history). Each tab
    is its own URL; htmx swaps `#api-body` and pushes the URL, and the
    list swaps `#api-table` with a cleaned `HX-Push-Url`.
  - Only APIs with a published version are listed; order is in use,
    deprecated, retired, then id. `q` is a case-insensitive substring of
    id or title, with LIKE wildcards escaped.
  - Rejected pushes appear in the history for the owning team and
    admins only, with error counts and the CI run link; their findings
    aren't shown in the UI (they're in the CI log).
  - "Lint & changes" is one tab: findings sorted by severity, then the
    diff against the baseline, with ack reasons inline.
  - Descriptions render as plain text (`white-space: pre-line`), not
    Markdown, for now.
  - Web pages are tested against Postgres (`pages_integration_test.go`),
    publishing through the real push API; the in-memory store only
    covers sign-in.
- **Docs (step 4, done):**
  - `/apis/{id}/versions/{v}/openapi.json` is a UI route (session auth),
    not `/api/v1`: the REST API still takes CI credentials only.
    `openapi.Document` loads the bundle with kin-openapi from memory,
    then `InternalizeRefs`; 3.1 round-trips. ETag = content hash +
    `documentVersion` (bump it when the output changes); 32 documents
    cached in memory.
  - Scalar 1.72.1 `standalone.js` is vendored as `static/scalar.js`
    (4.4 MB, served gzipped at 1.3 MB); the binary is now 100.9 MB. To
    upgrade: replace the file, update `LICENSES.txt`, and recheck
    `docs.js`'s options against the new version (fonts, telemetry,
    agent, MCP, `externalUrls`).
  - The Docs page's CSP adds `style-src 'nonce-…'` with a
    `<meta property="csp-nonce">`, which Scalar reads for its injected
    stylesheet. Scripts stay `'self'` only.
  - `app.css` is `@layer portal { … @scope (body) to (.scalar-app) { … } }`.
    New rules go inside the scope unless they must style `body`.
  - The Docs tab is a full page load (no `hx-get`). The mount point must
    stay empty (Scalar hydrates otherwise).
  - Scalar's API client ("Test request") is hidden: the CSP would block
    calls to the API's servers anyway.
- **Event page (step 5, done):**
  - `/events/{type}`: the declaring APIs come from `store.MessageRoles`
    (latest versions only), the consumers from `store.TypeConsumers`:
    `consumes` entries naming the type, or naming a declaring API with no
    `types`. 404 only when neither exists; a type that is only consumed
    renders with a "no producer published" banner.
  - Any second declaration, whatever its role, is a `ce-type-unique`
    conflict (03-formats: `receives` owns the contract too). The page
    shows the first (produces before receives, then by id) under an error
    banner listing the others.
  - Attributes, bindings and examples come from `version_models`. The
    schema documents aren't in it (`Schema.Doc` is `json:"-"`), so the
    tree re-parses the bundle with `check.ParseBundle`; parsed specs are
    cached by content hash (the generic `cache[V]`, 32 entries, shared
    with the OpenAPI documents). If that fails, the rest of the page still
    renders, with the error in place of the tree.
  - The tree (`web/schema.go`) is nested `<details>`, no JS, two levels
    open. `$ref`s resolve through `compat.Schema` (new exported `Top`,
    `Follow`, `Node.Child`), so it follows the same bundle rules as the
    compat checker. Properties are sorted by name (maps lose the file's
    order). Recursion stops with a note; 16 levels and 1,500 nodes at
    most. The referring schema's description wins over the target's.
  - Bindings link to a broker's `ui` for each of the owning API's
    environments on a broker of the binding's protocol. Binding props
    show as strings, or compact JSON for anything else.
  - `brokers` in `portal.config.yaml`: `name` (unique, required),
    `protocol`, `ui` (URL); protocol fields (`bootstrap`, `account`, …)
    are kept raw in `Settings`. The example config's kafka-prod got a `ui`.
- **Search (step 6, done):**
  - `store.Search(SearchQuery{Q, Kind, Team, API, Limit})`: candidates are
    a `UNION` of index-backed branches (tsquery on `tsv`; `title %> q`,
    i.e. `word_similarity`; `ILIKE` for queries under 3 characters), limited
    to latest versions of APIs that aren't retired. An `OR` would use
    neither GIN index.
  - Rank = `ts_rank_cd` + 2 × title word similarity + 1 for an exact title
    + 0.1 for the API's own document, × 0.5 when deprecated. Ties by api
    id, kind, ref.
  - `pg_trgm.word_similarity_threshold` is 0.45 (default 0.6 misses
    "refnd" → "refund", 0.5), set per transaction.
  - **`plan_cache_mode = force_custom_plan`** in the same transaction: pgx
    prepares the statement, and after five runs Postgres chose a generic
    plan that can't prune the UNION branches. p95 at 500 APIs went from
    6.2 s to 61 ms (limit 300 ms). Keep it if the query changes.
  - Snippets: `ts_headline` over `body`, for the returned rows only, with
    `\x01`/`\x02` delimiters that `web.highlight` turns into `<mark>`
    after escaping (ts_headline also drops tag-like text). The web layer
    collapses repeated adjacent words (the index holds identifiers as
    written and split) and hides a hit's snippet that marks nothing.
  - UI: `/search?q=&kind=&team=&api=`, 200 hits grouped by API in order
    of each API's best hit; the API's own document is the group heading;
    5 hits per group with "N more in …" (`api=` narrows to one API). Links:
    messages → `/events/{type}`, OpenAPI operations and schemas → the Docs
    tab, the rest → the version overview. htmx swaps `#search-results`
    as you type (250 ms) with a cleaned `HX-Push-Url` (`pushURL`, shared
    with `/apis`). A search box sits in the top bar except on home and
    search; hidden under 50rem.
  - Benchmark (`storetest.SeedAPIs`: 500 APIs, two versions each, 20
    operations or messages and 10 schemas, every 10th deprecated, every
    25th retired): 13–27 ms per search on the dev machine;
    `TestSearchLatency500` (in `task test:integration`, skipped with
    `-short`) fails at p95 ≥ 300 ms and logs the plan.
  - Not done: `GET /api/v1/search` and the older-versions filter (see
    [Known gaps](../STATUS.md#known-gaps)).
- **Diff page (step 7, done):**
  - `/apis/{id}/diff?from=&to=&view=changes|raw&mode=yaml|json&full=1`.
    `to` defaults to the latest, `from` to the published version below
    `to` by semver. Any two published versions can be compared, either
    way round (a note says when `from` is the later one). One version,
    the same content, or a `to` with nothing below it gives a message,
    not an error; an unknown version is a 404.
  - **Changes** come from `check.DiffBundles`: both bundles are unpacked
    side by side and go through the same `diff.Events` / `diff.OpenAPI`
    as a baseline check (`DiffFiles` now shares `diffSpecs`, which takes
    each side's root, so `$ref`s outside the entry's directory resolve).
    The API's current `compatibility` applies. Sorted breaking, warn,
    additive, docs. These are recomputed, so they can differ from the
    push's stored report if the rules changed since; the lint tab keeps
    showing the stored one.
  - **Raw diff** (`internal/textdiff`): go-udiff's `lcs.DiffLines` (gopls'
    algorithm), not the "hexops/gotextdiff" the plan named: that one is
    archived, and go-udiff was already in the module graph (unlinked); it
    adds 0.1 MB. Per file, unchanged files left out, added/removed files
    marked. "Canonical JSON" is each file as the content hash sees it
    (sorted keys, no comments), indented. A missing final newline isn't a
    change.
  - Folding: unchanged runs keep 3 lines of context and fold only if that
    hides more than 2 lines. A fold is its own `<tbody>`; htmx fetches
    `/apis/{id}/diff/lines?from&to&mode&file&start&end` (indices into the
    file's lines) and swaps it, cached `immutable`. Without JS the link
    is the page with `full=1`.
  - Everything for a pair (changes, both raw forms) is computed once and
    cached in memory by the two content hashes and the compatibility mode
    (`cache[*versionDiff]`, 32 entries).
  - Signs are drawn with CSS `::before`, so copying lines copies only
    their text.
  - Linked from the versions tab (a compare form, and each "from X" in
    the Changes column) and the lint tab ("Compare the files").
- **Acceptance tests (step 8, done):**
  - `internal/web/journeys_integration_test.go`: one test per journey,
    each step's page loaded as a signed-in user and checked for what the
    journey names, then its `<main>` compared with
    `testdata/journeys/*.html`. Only `<main>` is kept (the `<head>`
    carries static-file hashes that change with every CSS edit); blank
    lines and trailing spaces are dropped and push times replaced by
    `<time>`. Checked stable over repeated runs. `task test:journeys --
    -update` rewrites them; review the diff like any golden file.
  - `testdata/payments-service` (team-payments, repo `acme/payments`) is
    the second service the journeys need: `payments-http` with `POST
    /orders/{orderId}/refunds` and `Refund` schemas for J3's "refund",
    `payments-events` (`…payment.captured.v1`, `…refund.issued.v1`),
    consuming `orders-events`' `order.created.v1` for J4. It was kept out
    of `docs/spec/examples/` so the example's pinned hashes, scores and
    parser goldens stay as they are. The type names use the payments
    prefix, not J3's `com.acme.orders.refund.issued.v1`.
  - The test site pushes as any repo (`pushDir(repo, dir, edits…)`, one
    static token per repo).
  - J5 publishes a major (2.0.0) that removes a type (breaking), adds a
    payload field (additive) and rewords a summary (docs). Found and
    fixed: a removed message type linked to its event page, which is a
    404 once nothing declares it.
  - J3's latency target stays with `TestSearchLatency500`.
- **API title** (after M3): the descriptor's `title`, else the latest
  version's spec title (`version_models.model->>'title'`), in `ListAPIs`
  (shown and matched by `q`) and `APIDetail`; `APIDetail.Name()` falls back
  to the id. The heading and the page `<title>` use it; the crumbs keep the
  id.
- **Consumers by service** (after M3): `consumes` stays per descriptor
  (decided 2026-09-27; no per-API `consumes`). `store.Dependency` carries the
  consuming API's repo and owner, and `web.byRepo` groups the event page's
  "Consumed by" and the dependencies tab's "Used by" into one entry per
  repo: its CI subject, owner teams (linked to `/apis?team=`), the types
  (or "everything from"), and its APIs. The event page leaves out the types,
  which would only name the page's own type.
