# Decisions

Decisions taken during implementation where the [spec](../spec/README.md)
was silent, or where the implementation departs from it. Current progress
is in [STATUS.md](../STATUS.md); the roadmap's numbered decisions (D1–D14)
are in [06-roadmap](../spec/06-roadmap.md).

Add to the file for the area a change touches, and say why, not only what.
A decision that changes the spec belongs in the spec as well.

| File | Area |
|---|---|
| [check.md](check.md) | `portal check` (M1): baselines, content hash, compat, change ids, OpenAPI lint and diff, vacuum |
| [reports.md](reports.md) | Text, JSON, SARIF and JUnit output |
| [store.md](store.md) | Postgres schema, immutability, claims, latest |
| [server.md](server.md) | Push pipeline and `/api/v1`: request and response, claims, baselines, server-only rules, metadata updates |
| [ci-auth.md](ci-auth.md) | CI credentials: OIDC ID tokens, static tokens, refs, audit |
| [push-client.md](push-client.md) | `portal push` and `check --baseline-from` |
| [distribution.md](distribution.md) | Module path, `portal version`, release archives, container image |
| [web-ui.md](web-ui.md) | M3 web UI: stack, index, sign-in, pages, docs, event page, search, diff, acceptance tests |
| [onboarding.md](onboarding.md) | M4: `portal init`, `--events-from`, guide, CI template, J1 |
| [agents.md](agents.md) | Agent access: Markdown pages, `llms.txt`, personal access tokens, MCP, `init --agents` |
