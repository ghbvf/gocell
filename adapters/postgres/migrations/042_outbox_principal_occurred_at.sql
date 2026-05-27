-- F1: wire envelope Principal/OccurredAt durable persistence for outbox_entries.
-- Adds principal (JSONB) and occurred_at (TIMESTAMPTZ) to the transactional
-- outbox table so each entry carries the full outbox.PrincipalMetadata
-- (actorId/subjectId/tenantId/sessionId) and producer-clock OccurredAt across
-- the async relay boundary.
--
-- All new columns are NOT NULL DEFAULT sentinel values so existing rows are
-- backfilled safely without a table rewrite.
--
-- Consistency level: L1 LocalTx (DDL; no outbox involved).
-- Issue: #1042 batch F1 (wire envelope principal column persistence).
--
-- No down migration — per project policy, migrations are append-only.
-- Rollback requires manual: ALTER TABLE outbox_entries DROP COLUMN principal,
-- DROP COLUMN occurred_at;
--
-- Sentinel values:
--   '{}'::jsonb for principal indicates PrincipalMetadata was not injected by
--   the producer (W0 adoption transition). New binaries can detect un-adopted
--   rows by checking principal = '{}'.
--   Timestamp '1970-01-01 00:00:00+00' for occurred_at indicates the producer
--   did not set OccurredAt (zero-value time.Time semantic). New binaries may
--   rely on these sentinels for backward-fill detection.

ALTER TABLE outbox_entries
    ADD COLUMN principal   JSONB        NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN occurred_at TIMESTAMPTZ  NOT NULL DEFAULT '1970-01-01 00:00:00+00';
