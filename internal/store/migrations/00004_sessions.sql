-- +goose Up
-- Web UI sessions (docs/spec/05-architecture.md §Users: OIDC). The cookie
-- holds a random id; only its sha256 is stored.
CREATE TABLE sessions (
    id_hash      bytea PRIMARY KEY,
    subject      text NOT NULL,
    name         text NOT NULL DEFAULT '',
    email        text NOT NULL DEFAULT '',
    groups       text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
