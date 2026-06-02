-- Migration 048: create (namespace, trace_id) index on audit_entries CONCURRENTLY.
-- Split from 047 because CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- (goose wraps transactional migrations in BEGIN/COMMIT). The column itself is
-- added in 047 (transactional, safe to roll back atomically); this migration adds
-- only the index using the no-transaction directive so CONCURRENTLY is legal.
--
-- The index covers the exact-match filter AuditFilters.TraceID so trace-id
-- lookups are efficient without a sequential scan on audit_entries.
--
-- IF NOT EXISTS makes the Up step idempotent — safe to retry if interrupted.
-- An INVALID index residue from a prior interrupted CONCURRENTLY run is detected
-- by Migrator.Up via DetectInvalidIndexes before any migration executes.
--
-- ref: 047_audit_entries_trace_id.sql — column definition
-- ref: migrations/README.md rule 1 — CONCURRENTLY requires no-transaction annotation
-- ref: migrations/README.md rule 3 — Down path uses DROP INDEX CONCURRENTLY

-- +goose no transaction

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_namespace_trace_id
    ON audit_entries (namespace, trace_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_namespace_trace_id;
