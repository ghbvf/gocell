-- Migration 031: unique index on commands._idempotency_key to prevent TOCTOU
-- duplicate inserts under concurrent Enqueue calls.
--
-- Background: The prior implementation used a SELECT-then-INSERT two-step in
-- handleIdempotencyKey. Under concurrent load two goroutines racing through
-- that check could both see "no row" and both proceed to INSERT, producing
-- duplicate entries for the same idempotency key.
--
-- Fix: add a partial unique index on the JSONB expression
-- `metadata->>'_idempotency_key'` so the DB enforces uniqueness atomically.
-- The WHERE clause restricts the index to rows that actually carry the key,
-- keeping the index tight and avoiding interference with entries that have no
-- idempotency key (the common case).
--
-- The application-layer SELECT pre-check is removed (command_queue.go). The
-- DB unique violation (23505) is the sole dedup gate; the application layer
-- distinguishes between:
--   - PK violation (commands_pkey)          → ErrConflict (duplicate entry ID)
--   - Idempotency index violation (this one) → nil (idempotent no-op)
-- by inspecting pgErr.ConstraintName.
--
-- Non-CONCURRENTLY: commands is a new table with no existing data at deploy
-- time (this project has no external instances). Non-CONCURRENTLY runs in a
-- single transaction and avoids the CONCURRENTLY footgun on empty tables.
--
-- schema_guard.go registers this index in expectedIndexes.
--
-- ref: adapters/postgres/command_queue.go::insertEntry (constraint name handling)
-- ref: docs/plans/202605082145-034-pg-corecell-b-route-plan.md §B2.B

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE UNIQUE INDEX IF NOT EXISTS idx_commands_idempotency_key
    ON commands ((metadata->>'_idempotency_key'))
    WHERE metadata->>'_idempotency_key' IS NOT NULL;

-- +goose Down
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd
SET LOCAL lock_timeout = '5s';
DROP INDEX IF EXISTS idx_commands_idempotency_key;
