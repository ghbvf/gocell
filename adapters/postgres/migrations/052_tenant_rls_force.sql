-- Migration 052: enable FORCE ROW LEVEL SECURITY + tenant_isolation policy on
-- the configcore tenant tables (EPIC #1337 PR-3a, issue #1341).
--
-- This is the DB-kernel fail-closed backstop for multi-tenant row isolation.
-- The PR-2 application layer already filters every query by a typed TenantID
-- positional parameter (WHERE tenant_id = $param); this migration adds a second,
-- independent layer in PostgreSQL: even a raw / mis-written SQL statement that
-- omits the tenant predicate sees 0 rows when app.tenant_id is unset, and cannot
-- INSERT a row for a different tenant (WITH CHECK). The two layers are both
-- deliberate (spec D3 双层 fail-closed), not an old+new pair.
--
-- Tables (PR-3a scope — config three tables only):
--   config_entries, config_versions, feature_flags
--   All carry tenant_id TEXT NOT NULL (migration 051).
--
-- DELIBERATELY OUT OF SCOPE (tracked, NOT silently dropped):
--   - users / roles / role_assignments: deferred to PR-3b together with the
--     pre-auth/service ctx-tenant wiring (setup / login / refresh / rbacassign)
--     they require, to isolate the security-sensitive auth surgery.
--   - audit_entries: its hash chain is namespace-GLOBAL (UNIQUE(namespace,seq_no)
--     + tail/prev-hash/Verify read by namespace across tenants). Per-tenant RLS
--     would make the appender read a tenant-filtered tail and collide with the
--     global UNIQUE(namespace,seq_no) → append failure / chain corruption.
--     audit RLS requires first re-architecting the chain to per-(namespace,tenant);
--     deferred to #1618. Audit read isolation today is the app-layer
--     AuditFilters.TenantID (PR-2a).
--   - outbox / saga / refresh_tokens / sessions: no tenant_id column (framework
--     internal / deferred); not under RLS here.
--
-- Predicate (TEXT, NOT cast to uuid):
--   USING / WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
--   - current_setting(..., true) = missing_ok → returns NULL when the GUC was
--     never set (probes / migrations / any path that did not run SET LOCAL).
--   - NULLIF(..., '') maps the empty-string GUC to NULL too.
--   - tenant_id = NULL is never true → 0 rows visible AND 0 rows insertable
--     (fail-closed). TxManager.RunInTx sets the GUC via
--     set_config('app.tenant_id', $1, true) at transaction start.
--   The tenant_id columns are TEXT (migration 051), and the GUC value is a
--   Validate()-canonical lowercase UUID string, so a TEXT-to-TEXT compare is
--   exact and index-friendly. The issue text wrote `::uuid`, which assumed a
--   uuid column; casting only the setting (text = uuid) is a type error against
--   a TEXT column, and casting the column defeats the tenant_id index — so the
--   correct, index-preserving predicate is the uncast TEXT form above. The
--   SystemTenantID (nil UUID) system-config tier is read by setting the GUC to
--   that nil UUID (strict equality), so no OR-branch is needed.
--
-- WITH CHECK is stated explicitly (it defaults to USING when omitted) so the
-- write-side guard survives a future USING-only edit.
--
-- Non-destructive DDL: ENABLE / FORCE / CREATE POLICY add no column, drop
-- nothing, and rewrite no row, so this migration needs NO `+gocell
-- forward-rebuild` annotation and NO Go destructive permit (contrast 050/051).
-- lock_timeout is auto-injected by the migrator session locker (README rule 4) —
-- do NOT set it here. The Down section is a true reversible rollback (DROP
-- POLICY + DISABLE RLS; no data loss).
--
-- [F-B11] DEPLOYMENT REQUIREMENT (NOT enforced by this migration — role/GRANT is
-- provisioning, not a schema migration): the application PG role MUST NOT own
-- these tables AND MUST NOT have BYPASSRLS. FORCE ROW LEVEL SECURITY makes RLS
-- apply even to the table OWNER, but a BYPASSRLS (or superuser) role still
-- bypasses ALL row security. The integration suite asserts rolbypassrls=false.
--
-- ref: adapters/postgres/tx_manager.go (setLocalTenant — set_config GUC injection)
-- ref: adapters/postgres/migrations/051_configcore_tenant_id.sql (tenant_id columns)
-- ref: docs/plans/specs/1220-tenancy-abac-dataperm/ (spec FR-004 / tasks T3.1)

-- +goose Up

ALTER TABLE config_entries  ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_entries  FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config_entries
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

ALTER TABLE config_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE config_versions FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON config_versions
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

ALTER TABLE feature_flags   ENABLE ROW LEVEL SECURITY;
ALTER TABLE feature_flags   FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feature_flags
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- +goose Down
-- Reversible, non-destructive: drop the policy and disable RLS. DISABLE ROW LEVEL
-- SECURITY clears both the ENABLE and FORCE flags. No data is touched.

DROP POLICY IF EXISTS tenant_isolation ON feature_flags;
ALTER TABLE feature_flags   DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON config_versions;
ALTER TABLE config_versions DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON config_entries;
ALTER TABLE config_entries  DISABLE ROW LEVEL SECURITY;
