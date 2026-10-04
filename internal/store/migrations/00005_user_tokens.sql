-- +goose Up
-- Personal access tokens: read-only credentials a signed-in user mints for
-- their agents and scripts. Separate from ci_tokens, which publish for a
-- repo. Like a session, a token carries who minted it and their groups at
-- the time; only its sha256 is stored.
CREATE TABLE user_tokens (
    id           bigserial PRIMARY KEY,
    hash         bytea NOT NULL UNIQUE,
    subject      text NOT NULL,
    name         text NOT NULL DEFAULT '',
    email        text NOT NULL DEFAULT '',
    groups       text[] NOT NULL DEFAULT '{}',
    label        text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    last_used_at timestamptz
);
CREATE INDEX user_tokens_subject ON user_tokens (subject);

-- +goose Down
DROP TABLE user_tokens;
