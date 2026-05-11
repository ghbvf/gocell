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
--   first_used_at TIMESTAMPTZ NULL: set on the first grace re-use of a
--   rotated parent token.
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

-- Preflight: PR#528 intentionally removed the idle_expires_at DEFAULT. That
-- means 016 is valid only for an empty refresh_tokens table unless an operator
-- writes an explicit backfill migration. Fail with a clear message before the
-- NOT NULL ADD COLUMN rather than surfacing PostgreSQL's generic null-column
-- error. Also catch manual reruns against the old PR#388 column definition.
-- +goose StatementBegin
DO $$
DECLARE
    refresh_table_name CONSTANT TEXT := 'refresh_tokens';
    row_count BIGINT;
    existing_default TEXT;
BEGIN
    IF to_regclass(refresh_table_name) IS NULL THEN
        RETURN;
    END IF;

    SELECT pg_get_expr(d.adbin, d.adrelid)
      INTO existing_default
      FROM pg_attribute a
     JOIN pg_class c ON c.oid = a.attrelid
     LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
     WHERE c.oid = to_regclass(refresh_table_name)
       AND a.attname = 'idle_expires_at'
       AND NOT a.attisdropped;

    IF existing_default IS NOT NULL THEN
        RAISE EXCEPTION
            'migration 016 refused: idle_expires_at already exists with default %; environments that applied the old PR#388 016 must goose down -count 1 then goose up, or use an explicit follow-up migration',
            existing_default;
    END IF;

    IF existing_default IS NULL
       AND NOT EXISTS (
           SELECT 1
             FROM information_schema.columns
            WHERE table_schema = current_schema()
              AND table_name = refresh_table_name
              AND column_name = 'idle_expires_at'
       ) THEN
        SELECT count(*) INTO row_count FROM refresh_tokens;
        IF row_count > 0 THEN
            RAISE EXCEPTION
                'migration 016 refused: refresh_tokens has % rows; idle_expires_at is NOT NULL with no DEFAULT/backfill because the project is undeployed',
                row_count;
        END IF;
    END IF;
END $$;
-- +goose StatementEnd

-- ADD COLUMN statements run outside a transaction (NO TRANSACTION mode) so
-- they can be paired with CREATE INDEX CONCURRENTLY below. CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block; the goose NO TRANSACTION
-- directive at the top of this file lifts that constraint for the whole migration.
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
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- This migration uses NO TRANSACTION; each statement runs in its own implicit
-- transaction. The GUC is session-scope so it persists across all statements.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS idx_refresh_tokens_idle_expires;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS used_times;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS first_used_at;

ALTER TABLE refresh_tokens
    DROP COLUMN IF EXISTS idle_expires_at;
