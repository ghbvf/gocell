-- Migration 018: sessions table for runtime/auth/session.Store PG conform.
--
-- Backs adapters/postgres/session_store.go (S3+S5).
--
-- ADR-credential D1 (jti-only fingerprint): the session row stores a `jti`
-- reference (RFC 9068 §2.2.4) and an `authz_epoch_at_issue` snapshot. We do
-- NOT store any plaintext access token nor an HMAC fingerprint — DB leaks
-- therefore cannot be replayed.
--
-- ADR-credential D2 (AuthzEpoch ordering): authz_epoch_at_issue captures
-- users.authz_epoch at sign-in; validate paths join sessions ↔ users on
-- subject_id and reject when claim.epoch < user.authz_epoch.
--
-- ADR-credential D3 (append-only revoke): revoked_at is NULL while active.
-- Revoke / RevokeForSubject set it exactly once; the column is never reset.
--
-- ref: dexidp/dex storage/sql refresh_token / auth_request layout
-- ref: hashicorp/vault vault/token_store.go accessor + parent layout

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS sessions (
    id                    TEXT        PRIMARY KEY,
    -- subject_id references the user; CASCADE delete keeps schema consistent
    -- under user-delete (the runtime Store still calls RevokeForSubject in
    -- the same transaction; this CASCADE is the DB-level safety net).
    subject_id            UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- jti is the JWT jti claim reference (ADR-credential D1). UNIQUE so the
    -- protocol invariant "one server-side row per issued jti" is enforced
    -- by the DB and not just the application layer.
    jti                   TEXT        NOT NULL,
    authz_epoch_at_issue  BIGINT      NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    revoked_at            TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_jti
    ON sessions (jti);

-- subject-scope revoke and active-session listings; partial index keeps it
-- small (revoked rows trail off after the GC sweep retains them for audit).
CREATE INDEX IF NOT EXISTS idx_sessions_subject_active
    ON sessions (subject_id) WHERE revoked_at IS NULL;

-- GC / expiry sweep for the future PG janitor. Plain (non-partial) index so
-- the planner uses it for `DELETE FROM sessions WHERE expires_at < $1`
-- regardless of revocation status.
CREATE INDEX IF NOT EXISTS idx_sessions_expires
    ON sessions (expires_at);

-- +goose Down
-- WARNING: Irreversible — DROP TABLE forces logout for every active user
-- (sessions are non-recoverable once the table is dropped). Coordinate with
-- user communication before executing in any environment that has real
-- traffic.
DROP INDEX IF EXISTS idx_sessions_expires;
DROP INDEX IF EXISTS idx_sessions_subject_active;
DROP INDEX IF EXISTS idx_sessions_jti;
DROP TABLE IF EXISTS sessions;
