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
--     as TEXT hex strings (64 chars for SHA-256). ck_audit_hash_format enforces the
--     format at the DB layer: hash is always 64-char lowercase hex; prev_hash is
--     empty only for the genesis row (seq_no=1) and 64-char hex otherwise. It is
--     seq_no-coupled, so an empty prev_hash on a non-genesis row (or a non-empty
--     prev_hash on genesis) is rejected — a secondary guard alongside
--     uq_audit_namespace_seq and the 021 (namespace, event_id) index. It cannot
--     express chain linkage (prev_hash == predecessor hash); that is Verify's job.
--   - actor_id is always populated by the audit event producer; the appender's
--     extractActor() rejects events with a missing/empty actorId before they reach
--     the store (system events use a "system:..." actor). actor_id is a queryable,
--     non-PII principal identifier (filterable via auditquery, self-or-admin gated)
--     and is NOT redacted on the query output path.
--   - payload BYTEA stores the raw event payload bytes; strict JSON validation is
--     enforced in Go (validateAuditPayloadJSON) before the INSERT. BYTEA preserves
--     the exact byte sequence used as HMAC input, ensuring byte-for-byte equivalence
--     with the MemStore path (D1 invariant). No JSONB normalization occurs.
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
CREATE TABLE IF NOT EXISTS audit_entries (
    id           UUID        PRIMARY KEY,
    namespace    TEXT        NOT NULL,
    seq_no       BIGINT      NOT NULL,
    event_id     TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    actor_id     TEXT        NOT NULL,
    timestamp    TIMESTAMPTZ NOT NULL,
    payload      BYTEA       NOT NULL,
    prev_hash    TEXT        NOT NULL,
    hash         TEXT        NOT NULL,
    CONSTRAINT uq_audit_namespace_seq UNIQUE (namespace, seq_no),
    CONSTRAINT ck_audit_hash_format CHECK (
        (seq_no = 1 AND prev_hash = ''                AND hash ~ '^[0-9a-f]{64}$')
     OR (seq_no > 1 AND prev_hash ~ '^[0-9a-f]{64}$'  AND hash ~ '^[0-9a-f]{64}$')
    )
);

CREATE INDEX IF NOT EXISTS idx_audit_namespace_ts_id
    ON audit_entries (namespace, timestamp DESC, id ASC);

CREATE INDEX IF NOT EXISTS idx_audit_namespace_event_type
    ON audit_entries (namespace, event_type);

-- Future index additions on this table must use CREATE INDEX CONCURRENTLY (table no longer empty after first deploy).

-- +goose Down
-- WARNING: This down migration drops audit_entries and PERMANENTLY DELETES
-- all audit data. Production rollback MUST back up the table first
-- (e.g., `pg_dump -t audit_entries`). The down path is intended for
-- development/test environments where audit retention is not required.
--
-- Fail-closed: refuse destructive rollback unless gocell.allow_destructive_down is set.
-- Set by Migrator.Down's destructiveDownSessionLocker; direct goose CLI / psql bypass
-- without the GUC will RAISE EXCEPTION here.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_destructive_down', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'destructive down blocked: GUC gocell.allow_destructive_down not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS idx_audit_namespace_event_type;
DROP INDEX IF EXISTS idx_audit_namespace_ts_id;
DROP TABLE IF EXISTS audit_entries;
