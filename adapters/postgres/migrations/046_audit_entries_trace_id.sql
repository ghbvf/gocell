-- Migration 046: add trace_id column to audit_entries for OpenTelemetry
-- correlation (issue #1048, Batch 1-B).
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
--   - The index (namespace, trace_id) covers the exact-match filter
--     AuditFilters.TraceID so trace-id lookups are efficient. Uses
--     CREATE INDEX rather than CONCURRENTLY because this migration runs
--     against a table that may have data; the table is small in pre-v1.0
--     deployments (no production traffic yet). Operators on large tables
--     should migrate manually with CONCURRENTLY if needed.
--
-- ref: 043_audit_entries_v2.sql — NOT NULL no-DEFAULT pattern for correlation_id
-- ref: runtime/audit/ledger/protocol.go — auditHashInput struct (trace_id absent)

-- +goose Up

ALTER TABLE audit_entries ADD COLUMN trace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_entries ALTER COLUMN trace_id DROP DEFAULT;
CREATE INDEX idx_audit_namespace_trace_id ON audit_entries (namespace, trace_id);

-- +goose Down

DROP INDEX IF EXISTS idx_audit_namespace_trace_id;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS trace_id;
