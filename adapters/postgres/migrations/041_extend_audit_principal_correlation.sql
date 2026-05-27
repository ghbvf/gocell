-- B3: wire envelope principal/correlation columns for audit_entries.
-- Adds subject_id, tenant_id, session_id, correlation_id, occurred_at to the
-- ledger hash-chain table so each entry carries the full outbox.PrincipalMetadata
-- + outbox.ObservabilityMetadata.CorrelationID + producer-clock OccurredAt.
--
-- All new columns are NOT NULL DEFAULT '' / '1970-01-01 00:00:00+00' so existing
-- rows are backfilled with safe sentinel values without a table rewrite.
--
-- Consistency level: L1 LocalTx (DDL; no outbox involved).
-- Issue: #1042 batch B3.

ALTER TABLE audit_entries
    ADD COLUMN subject_id      TEXT        NOT NULL DEFAULT '',
    ADD COLUMN tenant_id       TEXT        NOT NULL DEFAULT '',
    ADD COLUMN session_id      TEXT        NOT NULL DEFAULT '',
    ADD COLUMN correlation_id  TEXT        NOT NULL DEFAULT '',
    ADD COLUMN occurred_at     TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00';
