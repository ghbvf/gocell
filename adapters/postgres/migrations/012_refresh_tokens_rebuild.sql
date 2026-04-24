-- Migration 012: rebuild refresh_tokens for the append-only selector/verifier model.
--
-- Replaces the in-place UPDATE state machine from migration 007/011 with an
-- append-only lineage: every Issue and every Rotate INSERTs a new row;
-- rotated_at and revoked_at are one-way timestamp flips; verifier_hash is
-- never mutated after INSERT.
--
-- Wire token format:   base64url(selector_16B) "." base64url(verifier_32B)
-- Stored credentials:  selector BYTEA (plaintext lookup key, 16 raw bytes — non-secret)
--                      verifier_hash BYTEA (SHA-256(verifier), 32 raw bytes)
--
-- A DB snapshot therefore contains no credential-equivalent data; preimage
-- resistance against a uniformly random 32-byte verifier is 2^256. Callers
-- compare verifier_hash in Go via subtle.ConstantTimeCompare, not in SQL.
--
-- CLAUDE.md states "当前只有 gocell 自身，没有外部调用方" — gocell has no live
-- production deployments. This migration drops and recreates the table; no
-- data is preserved. After a deploy every developer re-authenticates once.
--
-- ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + constant-time compare)
-- ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse cascade)
-- ref: zitadel/zitadel internal/api/oidc/token_refresh.go (revoke-on-use baseline)

-- +goose Up
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_refresh_tokens_expires;
DROP INDEX IF EXISTS idx_refresh_tokens_session;
DROP INDEX IF EXISTS idx_refresh_tokens_obsolete_active;
DROP INDEX IF EXISTS idx_refresh_tokens_token_active;
DROP INDEX IF EXISTS idx_refresh_tokens_token;
DROP TABLE IF EXISTS refresh_tokens;

CREATE TABLE refresh_tokens (
    id            UUID        PRIMARY KEY,
    parent_id     UUID        NULL REFERENCES refresh_tokens(id) ON DELETE SET NULL,
    session_id    TEXT        NOT NULL,
    subject_id    TEXT        NOT NULL,
    selector      BYTEA       NOT NULL,
    verifier_hash BYTEA       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    rotated_at    TIMESTAMPTZ NULL,
    revoked_at    TIMESTAMPTZ NULL
);

-- Active-row selector uniqueness: a selector may appear at most once in the
-- "live" (not rotated, not revoked) state. Rotation produces a new selector
-- for the child; the parent's selector remains but is no longer live, so
-- historical rows continue to match the non-partial idx_refresh_tokens_selector.
CREATE UNIQUE INDEX idx_refresh_tokens_selector_live
    ON refresh_tokens (selector)
    WHERE rotated_at IS NULL AND revoked_at IS NULL;

-- Non-partial selector index for Rotate's SELECT-by-selector lookup. Covers
-- rotated rows so reuse-detection can probe the full history.
CREATE INDEX idx_refresh_tokens_selector
    ON refresh_tokens (selector);

-- Cascade revocation by session_id lineage (RevokeSession + reuse-detection).
CREATE INDEX idx_refresh_tokens_session
    ON refresh_tokens (session_id);

-- Cascade revocation by subject_id (RevokeUser — logout-all, user delete).
CREATE INDEX idx_refresh_tokens_subject
    ON refresh_tokens (subject_id);

-- GC sweep — single time axis over both live and revoked rows.
CREATE INDEX idx_refresh_tokens_expires
    ON refresh_tokens (expires_at);

-- Lineage walk (optional — GC uses ON DELETE SET NULL; future audit tools may
-- traverse by parent_id).
CREATE INDEX idx_refresh_tokens_parent
    ON refresh_tokens (parent_id);

-- +goose Down
-- Recreate the pre-X11 schema shape. Token data is not recoverable.
DROP INDEX IF EXISTS idx_refresh_tokens_parent;
DROP INDEX IF EXISTS idx_refresh_tokens_expires;
DROP INDEX IF EXISTS idx_refresh_tokens_subject;
DROP INDEX IF EXISTS idx_refresh_tokens_session;
DROP INDEX IF EXISTS idx_refresh_tokens_selector;
DROP INDEX IF EXISTS idx_refresh_tokens_selector_live;
DROP TABLE IF EXISTS refresh_tokens;

CREATE TABLE refresh_tokens (
    id             BIGSERIAL   PRIMARY KEY,
    token          TEXT        NOT NULL,
    obsolete_token TEXT        NULL,
    session_id     TEXT        NOT NULL,
    subject_id     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    last_used      TIMESTAMPTZ NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ NULL
);
CREATE UNIQUE INDEX idx_refresh_tokens_token_active
    ON refresh_tokens (token) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX idx_refresh_tokens_obsolete_active
    ON refresh_tokens (obsolete_token)
    WHERE obsolete_token IS NOT NULL AND revoked_at IS NULL;
CREATE INDEX idx_refresh_tokens_session ON refresh_tokens (session_id);
CREATE INDEX idx_refresh_tokens_expires ON refresh_tokens (expires_at);
CREATE INDEX idx_refresh_tokens_token   ON refresh_tokens (token);
