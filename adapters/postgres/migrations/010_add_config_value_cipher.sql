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
-- Intentionally not implemented: this migration is forward-only because dropping
-- value_cipher / value_key_id / value_edk / value_nonce would destroy encrypted
-- data with no recovery path. Manual rollback required; see ADR below and
-- docs/backlog.md A19.
--
-- ADR: docs/architecture/202604191800-adr-config-value-encryption.md
SELECT 'migration 010 has no automatic rollback — see comment above' AS notice;
