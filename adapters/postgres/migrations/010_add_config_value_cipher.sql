-- +goose no transaction
-- +goose Up
-- Migration 010: Add cipher columns for sensitive config value encryption.
--
-- The existing `value` column is retained:
--   - sensitive=false:  value = plaintext, all cipher columns = NULL
--   - sensitive=true:   value = '' (empty), cipher columns populated
--   - legacy rows (pre-migration): value = plaintext, value_cipher = NULL
--     → admin migration tool converts these in place (plaintext_migration.go)
--
-- ref: docs/architecture/202604191800-adr-config-value-encryption.md

ALTER TABLE config_entries
    ADD COLUMN IF NOT EXISTS value_cipher BYTEA,
    ADD COLUMN IF NOT EXISTS value_key_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS value_edk    BYTEA,
    ADD COLUMN IF NOT EXISTS value_nonce  BYTEA;

ALTER TABLE config_versions
    ADD COLUMN IF NOT EXISTS value_cipher BYTEA,
    ADD COLUMN IF NOT EXISTS value_key_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS value_edk    BYTEA,
    ADD COLUMN IF NOT EXISTS value_nonce  BYTEA;

-- +goose Down
-- Intentional no-op: dropping cipher columns destroys encrypted values with no recovery path.
-- Production rollback: DBA manually renames columns per migration 010 header comments and ADR.
-- See ADR: docs/architecture/202604191800-adr-config-value-encryption.md
