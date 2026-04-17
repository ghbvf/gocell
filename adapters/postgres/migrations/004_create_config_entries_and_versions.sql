-- +goose no transaction
-- Migration 004: config_entries + config_versions tables.
-- Runs outside an explicit transaction so CREATE INDEX CONCURRENTLY is valid.
-- ref: pressly/goose StatementModifiers; Kratos data layer pattern.

-- +goose Up

CREATE TABLE IF NOT EXISTS config_entries (
    id         TEXT        NOT NULL,
    key        TEXT        NOT NULL,
    value      TEXT        NOT NULL DEFAULT '',
    sensitive  BOOLEAN     NOT NULL DEFAULT false,
    version    INT         NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT config_entries_pk PRIMARY KEY (id),
    CONSTRAINT config_entries_key_uq UNIQUE (key)
);

CREATE TABLE IF NOT EXISTS config_versions (
    id           TEXT        NOT NULL,
    config_id    TEXT        NOT NULL REFERENCES config_entries(id),
    version      INT         NOT NULL,
    value        TEXT        NOT NULL DEFAULT '',
    sensitive    BOOLEAN     NOT NULL DEFAULT false,
    published_at TIMESTAMPTZ,
    CONSTRAINT config_versions_pk PRIMARY KEY (id),
    CONSTRAINT config_versions_entry_version_uq UNIQUE (config_id, version)
);

-- Keyset pagination index on (key, id) for config_entries LIST queries.
-- Matches query.AppendKeyset sort columns used by config_repo.go List().
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_config_entries_key_id
    ON config_entries (key ASC, id ASC);

-- Descending version index for config_versions (GetVersion + rollback history).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_config_versions_config_version
    ON config_versions (config_id ASC, version DESC);

-- +goose Down

DROP INDEX CONCURRENTLY IF EXISTS idx_config_versions_config_version;
DROP INDEX CONCURRENTLY IF EXISTS idx_config_entries_key_id;
DROP TABLE IF EXISTS config_versions;
DROP TABLE IF EXISTS config_entries;
