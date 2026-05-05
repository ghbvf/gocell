-- Migration 016: add idle-expiry and grace-reuse-counter columns to refresh_tokens.
--
-- X12 (REFRESH-IDLE-EXPIRE): idle_expires_at TIMESTAMPTZ NOT NULL
--   Tracks a sliding idle-expiry deadline. Issue sets idle_expires_at = now +
--   Policy.MaxIdle. Rotate updates the child row's idle_expires_at to
--   now + Policy.MaxIdle (sliding window). Tokens whose idle_expires_at <
--   now are rejected as idle-expired even if expires_at > now.
--   Policy.MaxIdle is required (must be positive); the application layer
--   writes idle_expires_at explicitly on every Issue and Rotate.
--
-- X14 (REFRESH-GRACE-COUNTER): first_used_at + used_times
--   first_used_at TIMESTAMPTZ NULL: set on the first Rotate of each token.
--   used_times     INT          NOT NULL DEFAULT 0: incremented every time
--   the parent token is re-presented within the grace window. When used_times
--   reaches Policy.GraceMaxReuses, the next re-present triggers cascade revoke
--   even if the re-present is within the ReuseInterval window.
--
-- ref: ory/hydra persistence/sql/persister_oauth2.go (refresh_token_rotated column pattern)
-- ref: zitadel/zitadel internal/api/oidc/token_refresh.go (idle TTL per-request reset)
--
-- DEV NOTE: this migration was modified in place by PR#528 to drop the
-- idle_expires_at DEFAULT clause. goose v3 stores version_id+tstamp (not
-- file hash), so dev environments that already applied the PR#388 version
-- of 016 must run `goose down -count 1` then `goose up` to pick up the new
-- DDL — otherwise goose treats 016 as already applied and the change is a
-- silent no-op (ADD COLUMN IF NOT EXISTS makes it inert too). CI is
-- ephemeral; project undeployed; no production schema migration concern.

-- +goose NO TRANSACTION

-- +goose Up

-- ADD COLUMN statements run outside a transaction (NO TRANSACTION mode) so
-- they can be paired with CREATE INDEX CONCURRENTLY below. CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block; the +goose NO
-- TRANSACTION directive lifts that constraint for the whole migration.
-- idle_expires_at is NOT NULL with no DEFAULT (PR#528 dropped the
-- migration-time DEFAULT) — this only succeeds against an empty table.
-- See DEV NOTE at the top of this file for upgrade flow.
ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS idle_expires_at TIMESTAMPTZ NOT NULL;

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS first_used_at TIMESTAMPTZ NULL;

ALTER TABLE refresh_tokens
    ADD COLUMN IF NOT EXISTS used_times INT NOT NULL DEFAULT 0;

-- GC sweep index on idle_expires_at so the GC batch can efficiently find
-- idle-expired rows even when expires_at is still in the future.
-- CONCURRENTLY avoids a full-table lock on this hot-path table (every Rotate
-- inserts a row).  Must be outside a transaction block — guaranteed by NO
-- TRANSACTION above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_refresh_tokens_idle_expires
    ON refresh_tokens (idle_expires_at);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_refresh_tokens_idle_expires;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS used_times;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS first_used_at;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS idle_expires_at;
