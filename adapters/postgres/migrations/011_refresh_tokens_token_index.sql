-- Migration 011: add non-partial token index for Branch 2 lookups.
-- checkActiveStateSQL queries by token without revoked_at filter;
-- the existing partial index (idx_refresh_tokens_token_active) only covers
-- non-revoked rows. This index covers all rows for the state-check path.
-- ref: PR#213 review finding P1-Cx1.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_token
    ON refresh_tokens (token);

-- +goose Down
DROP INDEX IF EXISTS idx_refresh_tokens_token;
