-- +goose Up
-- M3: what the read UI and search need, derived from the stored bundles
-- (docs/spec/05-architecture.md §Storage). portal reindex rebuilds it.
-- Only published versions are indexed.

CREATE EXTENSION IF NOT EXISTS pg_trgm; -- trusted: the database owner can create it

-- The version the UI shows by default: the highest non-pre-release, else
-- the highest pre-release. Kept by store.Record.
ALTER TABLE apis ADD COLUMN latest_version_id bigint REFERENCES versions (id);

-- The parsed model.Spec, for rendering. Schema documents aren't in it; they
-- are read from the bundle.
CREATE TABLE version_models (
    version_id bigint PRIMARY KEY REFERENCES versions (id) ON DELETE CASCADE,
    model      jsonb NOT NULL
);

-- Thin rows for queries across APIs. api_id is denormalised for them.
CREATE TABLE messages (
    version_id bigint NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    api_id     text NOT NULL,
    type       text NOT NULL,
    role       text NOT NULL,
    summary    text NOT NULL DEFAULT '',
    deprecated boolean NOT NULL DEFAULT false,
    PRIMARY KEY (version_id, type, role)
);
CREATE INDEX messages_type ON messages (type);

CREATE TABLE operations (
    version_id   bigint NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    api_id       text NOT NULL,
    method       text NOT NULL,
    path         text NOT NULL,
    operation_id text NOT NULL DEFAULT '',
    summary      text NOT NULL DEFAULT '',
    deprecated   boolean NOT NULL DEFAULT false,
    PRIMARY KEY (version_id, method, path)
);

CREATE TABLE bindings (
    version_id bigint NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    api_id     text NOT NULL,
    type       text NOT NULL,
    role       text NOT NULL,
    protocol   text NOT NULL,
    address    text NOT NULL
);
CREATE INDEX bindings_version ON bindings (version_id);
CREATE INDEX bindings_address ON bindings (protocol, address);

-- The descriptor's consumes, per API: its current state, not per version.
CREATE TABLE dependencies (
    from_api text NOT NULL REFERENCES apis (id) ON DELETE CASCADE,
    to_api   text NOT NULL, -- may not be in the portal (yet)
    types    text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (from_api, to_api)
);
CREATE INDEX dependencies_to ON dependencies (to_api);

CREATE TABLE search_docs (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    version_id bigint NOT NULL REFERENCES versions (id) ON DELETE CASCADE,
    api_id     text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('api', 'operation', 'message', 'schema')),
    ref        text NOT NULL,
    title      text NOT NULL,
    terms      text NOT NULL DEFAULT '', -- identifiers split into words; weighted above body
    body       text NOT NULL DEFAULT '',
    tsv        tsvector GENERATED ALWAYS AS (
                   setweight(to_tsvector('english'::regconfig, terms), 'A') ||
                   setweight(to_tsvector('english'::regconfig, body), 'B')) STORED
);
CREATE INDEX search_docs_version ON search_docs (version_id);
CREATE INDEX search_docs_tsv ON search_docs USING gin (tsv);
CREATE INDEX search_docs_title ON search_docs USING gin (title gin_trgm_ops);

-- +goose Down
DROP TABLE search_docs, dependencies, bindings, operations, messages, version_models;
ALTER TABLE apis DROP COLUMN latest_version_id;
