-- Migration 011: add non-partial token index for Branch 2 lookups.
-- checkActiveStateSQL queries by token without revoked_at filter;
-- the existing partial index (idx_refresh_tokens_token_active) only covers
-- non-revoked rows. This index covers all rows for the state-check path.
--
-- Uses CONCURRENTLY to avoid blocking writes on a table that may already
-- contain data. CONCURRENTLY cannot run inside a transaction block, hence
-- the NO TRANSACTION annotation.
--
-- If this migration is interrupted, the index may be left in INVALID state.
-- Detect via pg_indexes joined with pg_class (relkind='i', NOT indisvalid).
-- Cleanup: drop idx_refresh_tokens_token with CONCURRENTLY, then re-run migration.
--
-- ref: PR#213 review finding P1-Cx1 + multi-seat review P1.

-- +goose Up
-- +goose NO TRANSACTION
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_token
    ON refresh_tokens (token);

-- +goose Down
-- +goose NO TRANSACTION
DROP INDEX CONCURRENTLY IF EXISTS idx_refresh_tokens_token;
