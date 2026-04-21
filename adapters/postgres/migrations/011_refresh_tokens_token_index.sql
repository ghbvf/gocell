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
-- Detect: SELECT indexname FROM pg_indexes WHERE indexname = 'idx_refresh_tokens_token'
--         INTERSECT
--         SELECT relname FROM pg_class WHERE relkind = 'i' AND NOT indisvalid;
-- Cleanup: DROP INDEX CONCURRENTLY IF EXISTS idx_refresh_tokens_token; then re-run migration.
--
-- ref: PR#213 review finding P1-Cx1 + multi-seat review P1.

-- +goose Up
-- +goose NO TRANSACTION
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_token
    ON refresh_tokens (token);

-- +goose Down
-- +goose NO TRANSACTION
DROP INDEX CONCURRENTLY IF EXISTS idx_refresh_tokens_token;
