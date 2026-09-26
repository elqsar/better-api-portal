-- +goose Up
-- Static CI tokens (docs/spec/05-architecture.md §CI: fallback static
-- tokens): who issued each one and when it was last used, for `portal admin
-- token list`.
ALTER TABLE ci_tokens
    ADD COLUMN created_by   text NOT NULL DEFAULT '',
    ADD COLUMN last_used_at timestamptz;

-- +goose Down
ALTER TABLE ci_tokens DROP COLUMN created_by, DROP COLUMN last_used_at;
