# Store

- **Tables in M2** are only what push and the gate need: teams, repos, apis,
  bundles, versions, lint/diff reports, CI tokens, audit. The normalised
  model rows, `search_docs`, `dependencies` and `jobs` come with M3; they can
  be rebuilt from the stored bundles.
- **Immutability** is a partial unique index on `(api_id, semver)` for
  published versions only, so a rejected push can be fixed and retried
  under the same version.
- **Claims** are made by a published push's own insert of the `apis` row
  (`ON CONFLICT … WHERE repo_id matches`), so a claim race can't give an id
  two owners. A rejected push claims nothing (`ErrUnclaimed` if the API
  doesn't exist) and changes no metadata.
- **Latest** is chosen by semver in Go (`x/mod/semver`), not by insertion
  order.
- pgx + goose cost 6.8 MB (87.4 → 94.2 MB) and 8 linked modules (76 → 84).
  `modernc.org/sqlite` in the binary comes from vacuum (`pb33f/doctor`), not
  goose.
