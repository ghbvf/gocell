-- Migration 044: add principal (JSONB) and occurred_at (TIMESTAMPTZ) columns to
-- outbox_entries for the sealed-construction principal-injection feature (#1229).
--
-- Runbook (DESTRUCTIVE FORWARD — operator steps required before `goose up`):
--   1. Drain relay: wait until `outbox_entries` CountPending == 0 and the
--      broker queue is empty. No in-flight messages will be lost — the relay
--      reads from outbox_entries, and after TRUNCATE there is nothing to relay.
--   2. Confirm all relay instances are stopped or will tolerate a full drain.
--   3. Run `goose up` — the migration TRUNCATEs outbox_entries then ADDs the
--      two NOT NULL columns (no DEFAULT sentinel; the writer always supplies
--      values; empty principal marshals to `{}`).
--
-- Rationale for TRUNCATE + ADD COLUMN rather than ADD COLUMN DEFAULT:
--   - NOT NULL with no DEFAULT cannot apply to existing rows in PostgreSQL
--     without a back-fill. The relay has already forwarded pending rows, so
--     there are no actionable pending rows in a drained deployment.
--   - GoCell ships only itself (no external callers, no rolling deploy
--     compatibility — see CLAUDE.md "Review 和重构时不考虑向后兼容"). A one-shot
--     TRUNCATE + schema upgrade is the correct DESTRUCTIVE-FORWARD pattern
--     (mirrors migration 043 for audit_entries).
--   - An empty principal marshals to `{}` — the writer always supplies the
--     column value from outbox.PrincipalMetadata (zero value encodes fine).
--
-- +goose Up
-- Guard: if any pending rows exist, require operator ack via GUC.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM outbox_entries LIMIT 1)
       AND current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'migration 044: outbox_entries is non-empty. Drain relay (CountPending==0, broker queue empty) then set GUC gocell.allow_destructive_down=true before running goose up.';
    END IF;
END $$;
-- +goose StatementEnd

TRUNCATE outbox_entries;

ALTER TABLE outbox_entries
    ADD COLUMN principal   JSONB        NOT NULL,
    ADD COLUMN occurred_at TIMESTAMPTZ  NOT NULL;

-- +goose Down
-- WARNING: This down migration drops the principal and occurred_at columns,
-- permanently deleting all principal identity and occurred_at data for
-- outbox_entries rows. Production rollback MUST back up the table first.
--
-- Fail-closed: requires gocell.allow_destructive_down GUC (shared with 001/003/etc).
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE outbox_entries
    DROP COLUMN IF EXISTS principal,
    DROP COLUMN IF EXISTS occurred_at;
