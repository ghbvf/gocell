-- +goose no transaction
-- +goose Up
-- Migration 008: Add cipher columns for sensitive config value encryption.
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
-- Rollback is intentionally disabled for this migration.
--
-- Dropping value_cipher / value_key_id / value_edk / value_nonce columns would
-- permanently destroy encrypted sensitive values with no recovery path.
-- Production rollback strategy: rename columns to _deprecated_YYYYMMDD (manual DBA action).
--
-- To proceed with a rollback in dev/CI, manually execute:
--   ALTER TABLE config_entries  RENAME COLUMN value_cipher TO value_cipher_deprecated_20260419;
--   ALTER TABLE config_versions RENAME COLUMN value_cipher TO value_cipher_deprecated_20260419;
-- (and similar for value_key_id / value_edk / value_nonce)
-- See ADR: docs/architecture/202604191800-adr-config-value-encryption.md
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'config_value_cipher rollback requires manual rename — see migration 008 header and ADR docs/architecture/202604191800-adr-config-value-encryption.md';
END $$;
-- +goose StatementEnd
