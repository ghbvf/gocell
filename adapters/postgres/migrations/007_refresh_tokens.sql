-- Migration 007: refresh_tokens table for the F2 opaque refresh token store.
-- Backs runtime/auth/refresh.Store (see docs/plans/202604191515-auth-federated-whistle.md §F2 C6).
-- Transactional migration (no CONCURRENTLY): SET LOCAL lock_timeout per migrations/README.md rule 4.
-- Soft-delete via revoked_at TIMESTAMPTZ enables audit retention + delayed GC sweep.
-- ref: dexidp/dex storage/storage.go RefreshToken layout.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id             BIGSERIAL   PRIMARY KEY,
    token          TEXT        NOT NULL,
    obsolete_token TEXT        NULL,
    session_id     TEXT        NOT NULL,
    subject_id     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ NULL
);

-- Active token uniqueness (revoked rows may share a historical token string for audit).
CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_token_active
    ON refresh_tokens (token) WHERE revoked_at IS NULL;

-- Grace-window / reuse-detection probe by previous generation.
CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_obsolete_active
    ON refresh_tokens (obsolete_token)
    WHERE obsolete_token IS NOT NULL AND revoked_at IS NULL;

-- Cascade revoke by session (Store.Revoke).
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_session
    ON refresh_tokens (session_id);

-- GC sweep (Store.GC) — single time axis over both active and revoked rows.
-- Non-partial so the planner uses it for `DELETE WHERE expires_at < $1`
-- regardless of revocation status. Plan §F2 GC contract alignment.
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires
    ON refresh_tokens (expires_at);

-- +goose Down
-- WARNING: Irreversible in production — DROP TABLE destroys all active refresh sessions.
-- Running this migration down forces logout for ALL users (tokens are non-recoverable
-- once the table is dropped). Coordinate with user communication / maintenance window
-- before executing in any environment that has real traffic.
DROP INDEX IF EXISTS idx_refresh_tokens_expires;
DROP INDEX IF EXISTS idx_refresh_tokens_session;
DROP INDEX IF EXISTS idx_refresh_tokens_obsolete_active;
DROP INDEX IF EXISTS idx_refresh_tokens_token_active;
DROP TABLE IF EXISTS refresh_tokens;
