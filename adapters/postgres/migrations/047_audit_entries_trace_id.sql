-- Migration 047: add trace_id column to audit_entries for OpenTelemetry
-- correlation (issue #1048, Batch C).
--
-- trace_id is an OBSERVABILITY column — it carries the OpenTelemetry trace id
-- from the outbox observability envelope so audit entries can be correlated with
-- distributed traces during incident investigation.
--
-- Design decisions:
--   - trace_id is deliberately NOT part of the HMAC hash chain
--     (Protocol.ComputeHash, auditHashInput). It is observability metadata, not
--     an audited fact. Adding it to the chain would make tamper-evidence dependent
--     on the presence / absence of a trace context, which is operationally fragile
--     (traces are absent in background jobs, tests, non-instrumented flows).
--   - NOT NULL with no DEFAULT after backfill: the DEFAULT '' sentinel is a
--     one-time backfill for existing rows. DROP DEFAULT forces the application to
--     supply the value explicitly for every new row, matching the correlation_id
--     NOT-NULL-no-default semantics established in 043_audit_entries_v2.sql.
--   - Backfilling '' does NOT break existing-row hashes: trace_id is excluded
--     from the HMAC chain, so existing rows retain their valid chain hashes.
--   - The (namespace, trace_id) index is created in a separate migration 048
--     using CREATE INDEX CONCURRENTLY to comply with the repository rule that
--     large-table indexes must not hold ACCESS EXCLUSIVE locks (migrations/README.md
--     rule 1). Migration 048 uses the no-transaction directive required by CONCURRENTLY.
--
-- ref: 043_audit_entries_v2.sql — NOT NULL no-DEFAULT pattern for correlation_id
-- ref: runtime/audit/ledger/protocol.go — auditHashInput struct (trace_id absent)
-- ref: 048_audit_entries_trace_id_index.sql — CONCURRENTLY index creation

-- +goose Up

ALTER TABLE audit_entries ADD COLUMN trace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_entries ALTER COLUMN trace_id DROP DEFAULT;

-- +goose Down

ALTER TABLE audit_entries DROP COLUMN IF EXISTS trace_id;
