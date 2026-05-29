-- Migration 044: add principal (JSONB) and occurred_at (TIMESTAMPTZ) columns to
-- outbox_entries for the sealed-construction principal-injection feature (#1229).
--
-- Runbook (DESTRUCTIVE FORWARD — operator steps required before `goose up`):
--   1. Stop all producer traffic, then drain the relay. The drain is complete
--      when there are ZERO undelivered rows:
--          SELECT count(*) FROM outbox_entries WHERE status <> 'published';   -- must be 0
--      Do NOT use CountPending / outbox_pending_depth as the drain signal: that
--      query counts only status='pending' rows whose backoff has elapsed
--      (next_retry_at <= now()), so it EXCLUDES rows still in retry backoff and
--      rows in 'claiming'. A green CountPending==0 can hide undelivered rows
--      that this TRUNCATE would destroy. The criterion above is the safe one —
--      every non-'published' row is still un-relayed.
--   2. Confirm all relay instances are stopped (so no row re-enters 'claiming'
--      between the check and `goose up`).
--   3. Run `goose up`. The Up block fails closed if any non-'published' row
--      remains, unless the operator explicitly accepts the loss by setting the
--      dedicated forward-rebuild GUC gocell.allow_outbox_rebuild=true.
--
-- GUC decoupling (mirror of migration 043_audit_entries_v2):
--   This is a forward (Up) destructive rebuild, NOT a rollback. It uses the
--   dedicated gocell.allow_outbox_rebuild GUC, NOT gocell.allow_destructive_down
--   (which gates Down/rollback sections). Sharing the down GUC for a forward
--   TRUNCATE would conflate "I am rolling back" with "I am rebuilding forward",
--   so the boundary is kept explicit at the runbook + audit-log layer — exactly
--   the decoupling 043 established for audit_entries.
--
-- Rationale for TRUNCATE + ADD COLUMN rather than ADD COLUMN DEFAULT:
--   - NOT NULL with no DEFAULT cannot apply to existing rows in PostgreSQL
--     without a back-fill. A drained deployment (step 1) has no undelivered
--     rows, and already-'published' rows are awaiting cleanup retention only —
--     safe to discard.
--   - GoCell ships only itself (no external callers, no rolling deploy
--     compatibility — see CLAUDE.md "Review 和重构时不考虑向后兼容"). A one-shot
--     TRUNCATE + schema upgrade is the correct DESTRUCTIVE-FORWARD pattern
--     (mirrors migration 043 for audit_entries).
--   - An empty principal marshals to `{}` — the writer always supplies the
--     column value from outbox.PrincipalMetadata (zero value encodes fine).
--
-- +goose Up
-- Forward-rebuild permit (decoupled from the destructive-down GUC). Fail closed
-- if any UNDELIVERED row (status <> 'published') would be destroyed, unless the
-- operator sets gocell.allow_outbox_rebuild=true. Already-'published' rows are
-- delivered and safe to TRUNCATE, so they do not block the migration.
-- +goose StatementBegin
DO $$
DECLARE
    undelivered bigint;
BEGIN
    SELECT count(*) INTO undelivered FROM outbox_entries WHERE status <> 'published';
    IF undelivered > 0
       AND current_setting('gocell.allow_outbox_rebuild', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'migration 044: outbox_entries has % undelivered row(s) (status <> published). Drain the relay until "SELECT count(*) FROM outbox_entries WHERE status <> ''published''" is 0, then re-run; or set GUC gocell.allow_outbox_rebuild=true to accept the loss.', undelivered;
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
