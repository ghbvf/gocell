-- Migration 053: enable FORCE ROW LEVEL SECURITY + tenant_isolation policy on
-- the accesscore tenant tables (EPIC #1337 PR-3b, issue #1617).
--
-- Follow-up to PR-3a (#1341, migration 052) which placed the configcore three
-- tables under RLS and delivered the reusable mechanism (tenant.WithScope /
-- TxManager.RunInTx set_config('app.tenant_id',$1,true) injection / schema_guard
-- verifyRLS). PR-3b extends the identical fail-closed DB-kernel backstop to the
-- accesscore tables, after the auth-path tenant wiring (login 2-tx split,
-- refresh derive-tenant-from-session, sessionvalidate txRunner, rbacassign
-- tenantId contract, setup scoped writes) makes every accesscore table access
-- run under a tenant scope.
--
-- Tables (PR-3b scope — accesscore three tables, all carry tenant_id TEXT NOT
-- NULL from migration 050):
--   users, roles, role_assignments
--
-- NOT in scope (deliberate, tracked — same rationale as migration 052):
--   - sessions: it is the PRE-AUTH bootstrap carrier. refresh/validate read the
--     session by its unguessable PK BEFORE any tenant scope is known, to LEARN
--     the tenant (sessions.tenant_id, migration 054). Placing sessions under RLS
--     would fail-closed those carrier reads (no GUC set yet → 0 rows). The
--     carrier's cross-tenant consistency is instead enforced by the composite FK
--     (tenant_id, subject_id) → users(tenant_id, id) added in migration 054.
--   - audit_entries: namespace-global hash chain; per-tenant RLS requires a
--     chain re-architecture first (#1618).
--
-- Predicate (TEXT, NOT cast to uuid) — identical to migration 052:
--   USING / WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
--   current_setting(..., true) = missing_ok → NULL when the GUC was never set
--   (probes / migrations / any path that did not run SET LOCAL); NULLIF(...,'')
--   maps the empty-string GUC to NULL; tenant_id = NULL is never true → 0 rows
--   visible AND 0 rows insertable (fail-closed). The tenant_id columns are TEXT
--   (migration 050) and the GUC value is a Validate()-canonical lowercase UUID
--   string, so TEXT=TEXT compare is exact and index-friendly (idx_users_tenant_id_id
--   etc.). WITH CHECK is stated explicitly so a future USING-only edit cannot
--   silently drop the write-side guard.
--
-- Non-destructive DDL (ENABLE / FORCE / CREATE POLICY add no column, drop
-- nothing, rewrite no row): NO `+gocell forward-rebuild` annotation and NO Go
-- destructive permit (contrast 050). The Down section is a true reversible
-- rollback (DROP POLICY + DISABLE RLS + NO FORCE RLS; no data loss).
--
-- [F-B11] DEPLOYMENT REQUIREMENT (NOT enforced by this migration — role/GRANT is
-- provisioning, not a schema migration): the application PG role MUST NOT own
-- these tables AND MUST NOT have BYPASSRLS (or be superuser). FORCE ROW LEVEL
-- SECURITY makes RLS apply even to the table OWNER, but a BYPASSRLS/superuser
-- role still bypasses ALL row security. The integration suite asserts
-- rolbypassrls=false against a restricted role; the runtime readyz current-role
-- precondition probe + the production restricted app-serving pool wiring are an
-- independent app-pool readiness dimension deferred to a follow-up (it would turn
-- the superuser-based dev /readyz red until the two-role deployment model exists).
--
-- ref: adapters/postgres/migrations/052_tenant_rls_force.sql (config-table RLS, copied)
-- ref: adapters/postgres/migrations/050_accesscore_tenant_id.sql (tenant_id columns)
-- ref: docs/plans/specs/1220-tenancy-abac-dataperm/ (spec FR-004)

-- +goose Up

ALTER TABLE users            ENABLE ROW LEVEL SECURITY;
ALTER TABLE users            FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON users
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

ALTER TABLE roles            ENABLE ROW LEVEL SECURITY;
ALTER TABLE roles            FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON roles
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

ALTER TABLE role_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE role_assignments FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON role_assignments
    USING      (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), ''));

-- +goose Down
-- Reversible, non-destructive: drop the policy, disable RLS, and clear FORCE.
-- DISABLE ROW LEVEL SECURITY only flips relrowsecurity (the ENABLE flag) — it
-- does NOT clear relforcerowsecurity (these are two independent pg_class
-- columns in PostgreSQL). NO FORCE ROW LEVEL SECURITY is therefore stated
-- explicitly so the Down truly restores the pre-migration catalog state
-- (relrowsecurity=false AND relforcerowsecurity=false); otherwise a dormant
-- FORCE flag would survive and re-applying ENABLE later (without re-FORCE)
-- would unexpectedly subject the table owner to RLS. No data is touched.

DROP POLICY IF EXISTS tenant_isolation ON role_assignments;
ALTER TABLE role_assignments NO FORCE ROW LEVEL SECURITY;
ALTER TABLE role_assignments DISABLE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON roles;
ALTER TABLE roles            NO FORCE ROW LEVEL SECURITY;
ALTER TABLE roles            DISABLE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON users;
ALTER TABLE users            NO FORCE ROW LEVEL SECURITY;
ALTER TABLE users            DISABLE  ROW LEVEL SECURITY;
