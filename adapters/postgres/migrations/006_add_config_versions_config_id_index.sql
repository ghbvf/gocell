-- +goose no transaction
-- Migration 006: config_versions.config_id single-column index for eq-lookup optimization.
-- CONCURRENTLY to avoid blocking writes; complements composite idx_config_versions_config_version.
-- ref: migrations/README.md rule 1 (CONCURRENTLY requires no-transaction annotation)

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_config_versions_config_id
    ON config_versions (config_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_config_versions_config_id;
