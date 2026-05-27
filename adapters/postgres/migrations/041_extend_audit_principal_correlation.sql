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
--
-- No down migration — per project policy, migrations are append-only.
-- Rollback requires manual: ALTER TABLE audit_entries DROP COLUMN subject_id,
-- DROP COLUMN tenant_id, DROP COLUMN session_id, DROP COLUMN correlation_id,
-- DROP COLUMN occurred_at;
--
-- Hash chain format (W1.2, PR #1218):
--   HMAC message = json.Marshal(auditHashInput{prev_hash, event_id, event_type,
--   actor_id, subject_id, tenant_id, session_id, correlation_id,
--   occurred_at_unix_nano, timestamp_unix_nano, payload}).
--   Canonical JSON encoding (struct source-declaration order) eliminates the
--   field-boundary collision risk of the previous pipe-separated format.
--   Payload is encoded as a base64 string by encoding/json []byte handling.
--
-- Sentinel values:
--   Empty string '' for string columns (subject_id, tenant_id, session_id,
--   correlation_id) indicates Principal was not injected by the producer
--   (W0 adoption transition). New binaries can detect un-adopted rows by
--   checking for the empty-string sentinel.
--   Timestamp '1970-01-01 00:00:00+00' for occurred_at indicates the producer
--   did not call InjectPrincipalFromContext (zero-value time.Time semantic).
--   New binaries may rely on these sentinels for backward-fill detection.

ALTER TABLE audit_entries
    ADD COLUMN subject_id      TEXT        NOT NULL DEFAULT '',
    ADD COLUMN tenant_id       TEXT        NOT NULL DEFAULT '',
    ADD COLUMN session_id      TEXT        NOT NULL DEFAULT '',
    ADD COLUMN correlation_id  TEXT        NOT NULL DEFAULT '',
    ADD COLUMN occurred_at     TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00';
