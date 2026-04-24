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
-- ATOMIC CUTOVER REQUIRED: this schema change is incompatible with
-- pre-PR-A29 pods. You cannot canary-deploy by routing traffic to mixed-
-- version pods while the migration runs — old pods write the legacy token
-- column format which this schema does not have, and new pods expect the
-- selector/verifier columns which the old schema does not have. Deploy
-- all pods atomically (blue/green or full rolling restart with migration
-- run first, old pods drained before new pods receive traffic).
--
-- ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + constant-time compare)
-- ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse cascade)
-- ref: zitadel/zitadel internal/api/oidc/token_refresh.go (revoke-on-use baseline)

-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Pre-flight row-count guard: fresh DBs proceed automatically, but an existing
-- refresh_tokens table with rows requires an explicit operator confirmation
-- because every active refresh session will be invalidated.
--
-- To allow this destructive rebuild on a known-safe database, run goose with
-- a connection option that sets:
--   gocell.allow_destructive_refresh_tokens_rebuild=true
-- Example libpq options:
--   options='-c gocell.allow_destructive_refresh_tokens_rebuild=true'
-- +goose StatementBegin
DO $$
DECLARE
    row_count bigint;
BEGIN
    IF to_regclass('refresh_tokens') IS NOT NULL THEN
        SELECT count(*) INTO row_count FROM refresh_tokens;
        IF row_count > 0
           AND lower(coalesce(current_setting('gocell.allow_destructive_refresh_tokens_rebuild', true), '')) <> 'true' THEN
            RAISE EXCEPTION 'migration 012 refused: refresh_tokens has % rows; set gocell.allow_destructive_refresh_tokens_rebuild=true only after an approved destructive cutover',
                row_count;
        END IF;
    END IF;
END $$;
-- +goose StatementEnd

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
-- WARNING: IRREVERSIBLE DATA LOSS.
-- production. Running down recreates the pre-PR-A29 schema (legacy token/
-- obsolete_token columns) which is INCOMPATIBLE with the new binary. The old
-- binary also cannot run against the 012 schema and may not embed this Down
-- migration. To rollback safely: drain/stop app traffic, run a maintenance
-- migrator built from the PR-A29 binary with the destructive-down flag, then
-- start the old binary after the DB schema is back at 011. Never run new and
-- old binaries against the opposite refresh_tokens schema.
--
-- Recreate the pre-X11 schema shape. Token data is not recoverable.
-- +goose StatementBegin
DO $$
BEGIN
    IF lower(coalesce(current_setting('gocell.allow_destructive_refresh_tokens_down', true), '')) <> 'true' THEN
        RAISE EXCEPTION 'migration 012 down refused: destructive rollback disabled; set gocell.allow_destructive_refresh_tokens_down=true only after old binary is deployed';
    END IF;
END $$;
-- +goose StatementEnd

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
