-- Migration 041: rebuild audit_entries as canonical v2 with Principal +
-- Correlation + OccurredAt columns and the 11-field HMAC chain.
--
-- This migration supersedes 020_audit_ledger.sql + 021_audit_entries_event_id_unique.sql
-- by **dropping and recreating** the audit_entries table with the full v2 column
-- set. No ALTER ADD. No sentinel DEFAULTs. New rows must supply explicit values
-- for every column. The chain restarts from seq_no = 1; old rows in the 020/021
-- schema cannot be hash-verified against the v2 HMAC format (auditHashInput JSON
-- canonical, 11 fields) and are intentionally discarded.
--
-- Rationale (per CLAUDE.md "Review 和重构时不考虑向后兼容——当前只有 gocell 自身"):
--   - PR #1218 attempted W0 transition with NOT NULL DEFAULT '' sentinels for
--     subject_id / tenant_id / session_id / correlation_id and
--     DEFAULT '1970-01-01 00:00:00+00' for occurred_at. The 14-finding review
--     traced the root cause to that incremental-adoption shape. This migration
--     replaces it with a one-shot canonical rewrite.
--   - The 11-field HMAC msg (auditHashInput typed struct, json.Marshal source-
--     order) is the only chain format. There is no hash_version column, no
--     sentinel hash backfill, no dual-write coexistence.
--
-- Design decisions (carried forward from 020):
--   - namespace TEXT NOT NULL partitions the chain by owner cell (e.g. "auditcore").
--   - seq_no is assigned inside a transaction serialized by
--     pg_advisory_xact_lock(hashtextextended(namespace, 0)) + SELECT FOR UPDATE
--     on the tail row.
--   - id UUID PRIMARY KEY is a stable opaque row handle.
--   - prev_hash + hash form the tamper-evident HMAC-SHA256 chain. Both are TEXT
--     hex strings (64 chars for SHA-256). ck_audit_hash_format enforces the
--     format at the DB layer; seq_no-coupled so the genesis row (seq_no=1) has
--     empty prev_hash and every other row has 64-char hex.
--   - payload BYTEA preserves exact byte sequence for HMAC equivalence with
--     MemStore. No JSONB normalization.
--   - UNIQUE (namespace, seq_no) enforces monotonic-per-namespace at the DB
--     level as a secondary guard.
--   - UNIQUE (namespace, event_id) provides the second-line guard against
--     concurrent application-layer fingerprint bypass (previously in 021).
--
-- Five new columns (canonical, NOT NULL, no DEFAULT — callers must supply):
--   - subject_id      OAuth subject-of-record (the end-user's stable identity).
--   - tenant_id       Tenant boundary identifier for multi-tenant deployments.
--   - session_id      Session identifier of the triggering request.
--   - correlation_id  Cross-cell correlation identifier from the outbox
--                     observability envelope.
--   - occurred_at     Producer-clock event time (distinct from timestamp which
--                     is the ledger persistence / HMAC time).
--
-- Indexes (names preserved from 020/021 so schema_guard.go expectedIndexes
-- entries and any operational tooling/dashboards keyed on these names continue
-- to work without rename):
--   - idx_audit_namespace_ts_id: covers timestamp-descending + id-ascending
--     keyset pagination (primary query shape).
--   - idx_audit_namespace_event_type: covers event_type filter queries.
--   - uq_audit_namespace_event_id: UNIQUE — DB-level dedup guard.
--
-- ref: google/trillian storage/postgres/log_storage.go — per-tree sequence number
-- ref: adapters/postgres/refresh_store.go — pg_advisory_xact_lock pattern
-- ref: tools/archtest/audit_hash_input_frozen_test.go AUDIT-HASH-INPUT-FROZEN-01

-- +goose Up
-- Forward-rebuild permit (decoupled from the destructive-down GUC). 041 is a
-- forward DROP+CREATE rebuild, NOT a Migrator.Down rollback, so it must not
-- share gocell.allow_destructive_down (which guards Down 020/021/etc). The
-- dedicated gocell.allow_audit_rebuild GUC makes the semantic boundary explicit
-- at the runbook + audit-log layer.
--
-- Up guard uses pg_class (not information_schema.tables) so the existence
-- check works regardless of whether the migration role has SELECT on
-- information_schema. Behavior matrix:
--   - table missing             → DROP+CREATE runs unconditionally (fresh deploy)
--   - table present, 0 rows     → DROP+CREATE runs unconditionally (idempotent dev re-run)
--   - table present, ≥1 row     → require GUC gocell.allow_audit_rebuild=true
--
-- Hardness ceiling: the GUC string is the strongest signal goose migration SQL
-- can read; a typed MigratorPermit funnel (Hard) would require moving forward-
-- rebuild orchestration out of the SQL file into Go-side Migrator.ForwardRebuild
-- API. Tracked as a backlog framework upgrade — see ADR-1042 §Decision 4 and
-- the follow-up issue cross-referenced there.
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('gocell.allow_audit_rebuild', true) IS DISTINCT FROM 'true'
       AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'audit_entries' AND relkind = 'r')
       AND EXISTS (SELECT 1 FROM audit_entries) THEN
        RAISE EXCEPTION 'audit_entries v2 rebuild blocked: existing rows present and GUC gocell.allow_audit_rebuild not set';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX IF EXISTS uq_audit_namespace_event_id;
DROP INDEX IF EXISTS idx_audit_namespace_event_type;
DROP INDEX IF EXISTS idx_audit_namespace_ts_id;
DROP TABLE IF EXISTS audit_entries;

CREATE TABLE audit_entries (
    id              UUID        PRIMARY KEY,
    namespace       TEXT        NOT NULL,
    seq_no          BIGINT      NOT NULL,
    event_id        TEXT        NOT NULL,
    event_type      TEXT        NOT NULL,
    actor_id        TEXT        NOT NULL,
    subject_id      TEXT        NOT NULL,
    tenant_id       TEXT        NOT NULL,
    session_id      TEXT        NOT NULL,
    correlation_id  TEXT        NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL,
    payload         BYTEA       NOT NULL,
    prev_hash       TEXT        NOT NULL,
    hash            TEXT        NOT NULL,
    CONSTRAINT uq_audit_namespace_seq UNIQUE (namespace, seq_no),
    CONSTRAINT ck_audit_hash_format CHECK (
        (seq_no = 1 AND prev_hash = ''                AND hash ~ '^[0-9a-f]{64}$')
     OR (seq_no > 1 AND prev_hash ~ '^[0-9a-f]{64}$'  AND hash ~ '^[0-9a-f]{64}$')
    )
);

CREATE INDEX        idx_audit_namespace_ts_id       ON audit_entries (namespace, timestamp DESC, id ASC);
CREATE INDEX        idx_audit_namespace_event_type  ON audit_entries (namespace, event_type);
CREATE UNIQUE INDEX uq_audit_namespace_event_id     ON audit_entries (namespace, event_id);

-- Future index additions on this table must use CREATE INDEX CONCURRENTLY
-- (table no longer empty after first deploy of 041).

-- +goose Down
-- 041 has no isolated Down path. A goose Down 041 would leave the system at
-- schema version 40 (the goose tracking table records the migration as
-- reverted) but audit_entries would be missing — the binary's schema_guard
-- expectedVersion=41 invariant would be violated and the application would
-- refuse to start. There is no half-state to preserve.
--
-- Forward-only rollback recipe (run by the operator, not goose Down 041):
--   1. goose down-to 020    (rewinds the tracking table to before audit_entries
--                            and DROPs the v2 table; safe because the v2 schema
--                            has no rows the operator wants to keep — its data
--                            was already discarded by the 041 Up rebuild)
--   2. goose up             (replays 020 + 021 + ... to restore the pre-041
--                            schema; the chain restarts from seq=1)
--
-- The Down block intentionally fail-fasts so any goose Down 041 invocation —
-- automation, manual psql, or test harness — exits with a clear error pointing
-- at the recipe above. This preserves forward-only canonical-rewrite semantics
-- (CLAUDE.md「Review 和重构时不考虑向后兼容」) and prevents the half-state
-- "version 40 / table missing" failure mode.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'audit_entries v2 (041) has no isolated Down — use `goose down-to 020 && goose up` to restore the pre-041 schema (see migration header for the recipe)';
END $$;
-- +goose StatementEnd
