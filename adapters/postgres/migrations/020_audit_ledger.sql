-- Migration 020: create audit_entries table for the ledger.Store PG backend.
--
-- Design decisions:
--   - namespace TEXT NOT NULL partitions the chain by owner cell (e.g. "auditcore").
--   - seq_no is NOT a SERIAL/IDENTITY column. The store assigns seq_no inside
--     a transaction serialized by pg_advisory_xact_lock(hashtextextended(namespace, 0))
--     + SELECT FOR UPDATE on the tail row. This guarantees a monotonic, gap-free
--     sequence per namespace without relying on SERIAL's own lock contention.
--   - id UUID PRIMARY KEY provides a stable opaque row handle for callers.
--   - prev_hash + hash form the tamper-evident HMAC-SHA256 chain. Both are stored
--     as TEXT hex strings (64 chars for SHA-256).
--   - payload JSONB stores the structured event payload; strict JSON validation is
--     enforced in Go before the INSERT.
--   - UNIQUE (namespace, seq_no) enforces the monotonic-per-namespace invariant at
--     the DB level as a secondary guard.
--
-- Indexes:
--   - idx_audit_namespace_ts_id: covers timestamp-descending + id-ascending keyset
--     pagination (primary query shape).
--   - idx_audit_namespace_event_type: covers event_type filter queries.
--
-- No CONCURRENTLY here; this is a new table with no concurrent reads at migration
-- time, so regular CREATE INDEX is safe and avoids the no-transaction requirement.
--
-- ref: google/trillian storage/postgres/log_storage.go — per-tree sequence number
-- ref: adapters/postgres/refresh_store.go — pg_advisory_xact_lock advisory lock pattern

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS audit_entries (
    id           UUID        PRIMARY KEY,
    namespace    TEXT        NOT NULL,
    seq_no       BIGINT      NOT NULL,
    event_id     TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    actor_id     TEXT        NOT NULL,
    timestamp    TIMESTAMPTZ NOT NULL,
    payload      JSONB       NOT NULL,
    prev_hash    TEXT        NOT NULL,
    hash         TEXT        NOT NULL,
    CONSTRAINT uq_audit_namespace_seq UNIQUE (namespace, seq_no)
);

CREATE INDEX IF NOT EXISTS idx_audit_namespace_ts_id
    ON audit_entries (namespace, timestamp DESC, id ASC);

CREATE INDEX IF NOT EXISTS idx_audit_namespace_event_type
    ON audit_entries (namespace, event_type);

-- Future index additions on this table must use CREATE INDEX CONCURRENTLY (table no longer empty after first deploy).

-- +goose Down
DROP INDEX IF EXISTS idx_audit_namespace_event_type;
DROP INDEX IF EXISTS idx_audit_namespace_ts_id;
DROP TABLE IF EXISTS audit_entries;
