-- Migration 044: add principal (JSONB) and occurred_at (TIMESTAMPTZ) columns to
-- outbox_entries for the sealed-construction principal-injection feature (#1229).
--
-- Runbook (DESTRUCTIVE FORWARD — operator steps required before `goose up`):
--   1. Stop all producer traffic, then drain the relay. The drain is complete
--      when this direct status count returns ZERO undelivered rows:
--
--          SELECT count(*) AS undelivered
--          FROM outbox_entries
--          WHERE status <> 'published';
--
--      Do NOT use CountPending / outbox_pending_depth as the drain signal: that
--      query counts only status='pending' rows whose backoff has elapsed
--      (next_retry_at <= now()), so it EXCLUDES rows still in retry backoff and
--      rows in 'claiming'. A green CountPending==0 can hide undelivered rows
--      that this TRUNCATE would destroy. The criterion above is the safe one —
--      every non-'published' row is still un-relayed.
--   2. Confirm all relay instances are stopped (so no row re-enters 'claiming'
--      between the check and `goose up`).
--   3. Run `pg-migrate -dsn "$GOCELL_PG_DSN" -rebuild "44:<reason>"`. The Up
--      block gate (forward-rebuild permit) is enforced in Go via
--      Migrator.ForwardRebuild + ForwardRebuildPermit (issue #1248).
--
-- Permit decoupling (mirror of migration 043_audit_entries_v2):
--   This is a forward (Up) destructive rebuild, NOT a rollback. The forward-rebuild
--   permit is intentionally separate from the destructive-down permit so that
--   rebuild approval is decoupled from rollback approval — exactly the decoupling
--   043 established for audit_entries.
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
-- Forward-rebuild gate is enforced in Go (Migrator.ForwardRebuild + ForwardRebuildPermit); see issue #1248.
-- +gocell forward-rebuild target=outbox_entries

TRUNCATE outbox_entries;

ALTER TABLE outbox_entries
    ADD COLUMN principal   JSONB        NOT NULL,
    ADD COLUMN occurred_at TIMESTAMPTZ  NOT NULL;

-- +goose Down
-- WARNING: This down migration drops the principal and occurred_at columns,
-- permanently deleting all principal identity and occurred_at data for
-- outbox_entries rows. Production rollback MUST back up the table first.
--
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

ALTER TABLE outbox_entries
    DROP COLUMN IF EXISTS principal,
    DROP COLUMN IF EXISTS occurred_at;
