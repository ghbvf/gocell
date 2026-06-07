-- Migration 055: re-architect audit_entries as a per-(namespace, tenant) hash
-- chain and put it under FORCE ROW LEVEL SECURITY (#1618; EPIC #1337 PR-3a
-- deferred item).
--
-- WHY a rebuild, not an ALTER: the pre-055 chain is namespace-GLOBAL
-- (UNIQUE(namespace, seq_no); tail/prev-hash/Verify read by namespace+seq_no
-- across all tenants). Under per-tenant RLS the appender (GUC = the event's
-- tenant) would read a tenant-filtered tail → compute seq_no = tenant_max+1 →
-- collide with the global UNIQUE → append failure / chain corruption. The chain
-- must become per-(namespace, tenant) first. Existing rows CANNOT be migrated:
-- their prev_hash linkage follows the original GLOBAL append order, so splitting
-- them into per-tenant sub-chains would break Verify (a tenant-T entry's
-- prev_hash points at the previous GLOBAL entry, which may belong to tenant S).
-- Per CLAUDE.md "Review 和重构时不考虑向后兼容——当前只有 gocell 自身" the rows are
-- intentionally discarded (same precedent as 043 superseding 020/021).
--
-- The 12-field HMAC chain format is UNCHANGED: seq_no is NOT part of the hash
-- (chain order is the prev_hash linkage), and tenant_id is already field 7. So
-- re-numbering seq_no per-tenant does not touch any hash; protocol.go and
-- audit_hash_input_frozen_test.go are unaffected. Cross-tenant HMAC replay is
-- already prevented by tenant_id participating in the digest.
--
-- Changes vs 043:
--   - UNIQUE (namespace, tenant_id, seq_no) — per-(namespace, tenant) monotonic
--     seq_no; each (namespace, tenant) sub-chain restarts at seq_no = 1 with
--     prev_hash = '' (genesis). The hash-format CHECK is unchanged (it is
--     seq_no-coupled, valid per-tenant).
--   - uq_audit_ns_tenant_event_id (namespace, tenant_id, event_id) — per-tenant
--     idempotency dedup (matches the appender's GUC-filtered RLS visibility).
--   - advisory lock is keyed on (namespace, tenant_id) two-key int4 form in Go
--     (pg_advisory_xact_lock(hashtext(ns), hashtext(tenant_id))).
--   - The keyset / event_type / trace_id indexes stay namespace-leading: the
--     tenant predicate is the disjunction (tenant_id = '' OR tenant_id = $X) and
--     the RLS USING predicate is itself that disjunction, which a tenant-leading
--     index cannot serve as a single ordered scan.
--
-- RLS (the point of this migration):
--   - ENABLE + FORCE ROW LEVEL SECURITY. FORCE makes the policy apply even to the
--     table OWNER; the serving role gocell_app is NOSUPERUSER NOBYPASSRLS (#1676)
--     so it cannot bypass. The integration suite asserts rolbypassrls=false.
--   - The tenant_isolation policy differs from config/accesscore (052/053): it
--     carries `OR tenant_id = ''`. This is LOAD-BEARING:
--       * USING: a tenant admin must be able to READ tenant-less system/framework
--         rows (bootstrap.auth.fail and other pre-auth events with no principal
--         tenant) in addition to its own tenant.
--       * WITH CHECK: the system/bootstrap appender runs with the GUC UNSET (no
--         principal tenant) → NULLIF(NULL,'') → NULL → `'' = NULL` is false, so a
--         plain predicate would REJECT every system-row insert (SQLSTATE 42501).
--         The `OR tenant_id = ''` clause admits them.
--     The write-side `OR tenant_id = ''` lets a tenant-scoped writer technically
--     INSERT a tenant_id='' row; this is bounded by the appender being the SOLE
--     audit_entries writer (AUDITCORE-APPENDER-SINGLE-SOURCE-01, Hard) — there is
--     no tenant-controlled INSERT path. Accepted, documented in the ADR.
--   - schema_guard.go verifyRLS pins this variant via the SystemRowsReadable flag
--     + rlsTenantWithSystemPredicateRe (still rejects `OR true` / wrong column /
--     missing NULLIF).
--
-- ref: adapters/postgres/migrations/043_audit_entries_v2.sql — table + chain template
-- ref: adapters/postgres/migrations/052_tenant_rls_force.sql — ENABLE/FORCE/policy template
-- ref: docs/architecture/202606071200-1676-adr-restricted-app-serving-pool.md — NOBYPASSRLS serving role

-- +goose Up
-- Forward-rebuild gate is enforced in Go (Migrator.ForwardRebuild + ForwardRebuildPermit); see issue #1248.
-- +gocell forward-rebuild target=audit_entries

DROP INDEX IF EXISTS idx_audit_namespace_trace_id;
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
    trace_id        TEXT        NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL,
    payload         BYTEA       NOT NULL,
    prev_hash       TEXT        NOT NULL,
    hash            TEXT        NOT NULL,
    CONSTRAINT uq_audit_namespace_tenant_seq UNIQUE (namespace, tenant_id, seq_no),
    CONSTRAINT ck_audit_hash_format CHECK (
        (seq_no = 1 AND prev_hash = ''                AND hash ~ '^[0-9a-f]{64}$')
     OR (seq_no > 1 AND prev_hash ~ '^[0-9a-f]{64}$'  AND hash ~ '^[0-9a-f]{64}$')
    )
);

CREATE INDEX        idx_audit_namespace_ts_id       ON audit_entries (namespace, timestamp DESC, id ASC);
CREATE INDEX        idx_audit_namespace_event_type  ON audit_entries (namespace, event_type);
CREATE UNIQUE INDEX uq_audit_ns_tenant_event_id     ON audit_entries (namespace, tenant_id, event_id);
CREATE INDEX        idx_audit_namespace_trace_id    ON audit_entries (namespace, trace_id);

-- Future index additions on this table must use CREATE INDEX CONCURRENTLY
-- (table no longer empty after first deploy of 055).

ALTER TABLE audit_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_entries FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_entries
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), '') OR tenant_id = '')
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '') OR tenant_id = '');

-- +goose Down
-- WARNING: This down migration drops audit_entries and PERMANENTLY DELETES all
-- audit data. Production rollback MUST back up the table first
-- (e.g., `pg_dump -t audit_entries`).
--
-- Destructive-down gate is enforced in Go (Migrator.Down + DestructiveDownPermit); see issue #1248.

DROP POLICY IF EXISTS tenant_isolation ON audit_entries;
ALTER TABLE audit_entries DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS idx_audit_namespace_trace_id;
DROP INDEX IF EXISTS uq_audit_ns_tenant_event_id;
DROP INDEX IF EXISTS idx_audit_namespace_event_type;
DROP INDEX IF EXISTS idx_audit_namespace_ts_id;
DROP TABLE IF EXISTS audit_entries;
