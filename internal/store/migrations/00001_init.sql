-- +goose Up
-- The M2 subset of docs/spec/05-architecture.md §Storage: what push and the
-- gate need. The normalised model rows, search, dependencies and jobs come
-- with M3; they can be rebuilt from the stored bundles.

CREATE TABLE teams (
    slug       text PRIMARY KEY,
    name       text NOT NULL,
    oidc_group text NOT NULL DEFAULT ''
);

CREATE TABLE repos (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ci_subject text NOT NULL UNIQUE, -- e.g. the GitHub "repository" claim
    url        text NOT NULL DEFAULT ''
);

CREATE TABLE apis (
    id        text PRIMARY KEY,
    kind      text NOT NULL,
    owner     text NOT NULL,
    lifecycle text NOT NULL DEFAULT '',
    sunset    date,
    repo_id   bigint NOT NULL REFERENCES repos (id),
    meta      jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE bundles (
    content_hash text PRIMARY KEY,
    data         bytea NOT NULL,
    size         bigint NOT NULL
);

CREATE TABLE versions (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    api_id       text NOT NULL REFERENCES apis (id),
    semver       text NOT NULL,
    prerelease   boolean NOT NULL,
    content_hash text NOT NULL REFERENCES bundles (content_hash),
    status       text NOT NULL CHECK (status IN ('published', 'rejected')),
    source       jsonb NOT NULL DEFAULT '{}',
    acks         jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);
-- Only published versions are immutable per semver; a rejected push can be
-- retried with the same version once it is fixed.
CREATE UNIQUE INDEX versions_published ON versions (api_id, semver) WHERE status = 'published';
CREATE INDEX versions_api ON versions (api_id, created_at DESC);

CREATE TABLE lint_reports (
    version_id   bigint PRIMARY KEY REFERENCES versions (id) ON DELETE CASCADE,
    ruleset_hash text NOT NULL DEFAULT '',
    score        int NOT NULL,
    findings     jsonb NOT NULL
);

CREATE TABLE diff_reports (
    version_id       bigint PRIMARY KEY REFERENCES versions (id) ON DELETE CASCADE,
    baseline_version text NOT NULL,
    changes          jsonb NOT NULL
);

CREATE TABLE ci_tokens (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id    bigint NOT NULL REFERENCES repos (id),
    hash       bytea NOT NULL UNIQUE, -- sha256 of the token; the token itself is never stored
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE TABLE audit_log (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor   text NOT NULL,
    action  text NOT NULL,
    target  text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}',
    at      timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE audit_log, ci_tokens, diff_reports, lint_reports, versions, bundles, apis, repos, teams;
