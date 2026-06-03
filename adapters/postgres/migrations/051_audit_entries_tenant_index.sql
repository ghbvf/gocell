-- Migration 051: add tenant-aware keyset pagination index on audit_entries.
--
-- The existing keyset index idx_audit_namespace_ts_id on (namespace, timestamp DESC, id ASC)
-- supports per-namespace keyset pagination. PR-2a adds tenant-scoped audit reads
-- (auditquery handler sets AuditFilters.TenantID). With a tenant_id predicate in
-- the WHERE clause, the planner cannot use the namespace-only index efficiently:
-- it must scan all rows for the namespace and then filter by tenant_id.
--
-- This index adds tenant_id as the second leading key, supporting queries of the
-- form:
--   WHERE namespace = $1 AND tenant_id = $2
--   ORDER BY timestamp DESC, id ASC
--
-- This is the primary query shape emitted by auditquery after PR-2a.
--
-- Index name: idx_audit_namespace_tenant_ts_id
--
-- IF NOT EXISTS makes the Up step idempotent — safe to retry if interrupted.
-- An INVALID index residue from a prior interrupted CONCURRENTLY run is detected
-- by Migrator.Up via DetectInvalidIndexes before any migration executes.
--
-- The Down path drops the index CONCURRENTLY to avoid ACCESS EXCLUSIVE locks on
-- a potentially large table.
--
-- ref: 043_audit_entries_v2.sql — table definition + comment "Future index additions
--      on this table must use CREATE INDEX CONCURRENTLY"
-- ref: 048_audit_entries_trace_id_index.sql — CONCURRENTLY no-transaction pattern
-- ref: adapters/postgres/schema_guard.go expectedIndexes — registry entry added

-- +goose no transaction

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_namespace_tenant_ts_id
    ON audit_entries (namespace, tenant_id, timestamp DESC, id ASC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_audit_namespace_tenant_ts_id;
