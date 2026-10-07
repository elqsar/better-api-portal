# CI auth

- **One `Authorization: Bearer` header** for both kinds: a `ptk_` prefix
  means a static token, anything else is verified as an ID token. The issuer
  is read from the unverified `iss` to pick the verifier; only exact
  matches of configured issuers are trusted.
- **Repo = `repoPrefix` + repo claim.** Several issuers must have distinct
  prefixes (config load fails otherwise), so a GitLab project can't push to
  the GitHub repo with the same path. Static tokens use the same CI subject,
  so a repo can switch methods and keep its APIs.
- **Refs gate pushing, not authentication.** A ref outside `allowedRefs`
  (default `refs/heads/main`, `refs/tags/*`, `path.Match` patterns, so `*`
  doesn't cross `/`) still checks and downloads baselines; a push gets 403.
  `Identity.CanPush` carries this. GitLab refs are qualified with
  `ref_type`.
- **Audit:** the actor is the token's `sub` (e.g.
  `repo:acme/orders:ref:refs/heads/main`) or `token:<id>`. The run URL is
  derived for github.com (`run_id`) and GitLab (`pipeline_id`). Static-token
  callers may send `X-Portal-Ref`, `-Commit`, `-Run-URL`, recorded
  unchecked.
- **An unreachable issuer** is a 500, not a 401, and isn't cached, so it's
  retried on the next request.
- **Static tokens:** 32 random bytes, sha256 stored, default 90 days,
  `created_by` and `last_used_at` (migration 00002). `portal admin token
  create|list|revoke`, audited as `token.create`/`token.revoke` with actor
  `cli:<os user>`. `httpapi.NoAuth` is gone; `serve` always runs `auth.CI`.
- go-oidc, go-jose and oauth2 are 3 new modules; the binary grows about
  1.5 MB (94.2 → 95.7 MB).
