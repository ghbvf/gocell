-- Migration 064: role-scoped audit admin read policy for cross-tenant audit reads
-- (#1810 super-admin cross-tenant audit read, DB layer).
--
-- WHY a separate permissive SELECT policy (not BYPASSRLS):
--   PostgreSQL PERMISSIVE policies are OR-ed together per role. A policy that
--   names a specific role (TO gocell_audit_admin) applies ONLY to that role —
--   gocell_app's effective predicate is unchanged, and FORCE ROW LEVEL SECURITY
--   stays fully intact for both roles. The serving role gocell_app never gains
--   cross-tenant visibility; the ADR #1676 no-BYPASSRLS invariant is preserved.
--
-- WHY role-existence guards (IF EXISTS in pg_roles):
--   Role creation belongs to the provisioning layer (deploy/postgres/init/);
--   not every environment (dev memory mode, testcontainer migration runs) has
--   gocell_audit_admin. Wrapping every DDL step in an existence guard keeps this
--   migration a strict no-op in those environments. The guard mirrors the
--   projection_events REVOKE pattern from migration 058.
--
-- DEPLOYMENT REQUIREMENT: gocell_audit_admin must be created BEFORE migrations
--   run. In deployments using deploy/postgres/init/10-restricted-role.sh the
--   role is provisioned there. The migration is inert (no policy, no grant)
--   wherever the role is absent.
--
-- schema_guard.go verifyRLS / checkRLSPolicyShape is re-scoped to expect this
--   second policy on audit_entries WHEN gocell_audit_admin is provisioned, and
--   to REJECT the policy when the role is absent — preserving the no-unexpected-
--   permissive-policy invariant (#1622 F1) for every table and for any policy
--   on audit_entries that is NOT in the expected set.
--
-- ref: adapters/postgres/migrations/055_audit_entries_per_tenant_rls.sql — table + RLS shape
-- ref: adapters/postgres/migrations/058_create_projection_events.sql — role-existence guard idiom
-- ref: docs/architecture/202606071200-1676-adr-restricted-app-serving-pool.md — NOBYPASSRLS posture

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocell_audit_admin') THEN
    EXECUTE 'CREATE POLICY audit_admin_read_all ON audit_entries
               FOR SELECT
               TO gocell_audit_admin
               USING (true)';
    EXECUTE 'GRANT SELECT ON audit_entries TO gocell_audit_admin';
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocell_audit_admin') THEN
    EXECUTE 'DROP POLICY IF EXISTS audit_admin_read_all ON audit_entries';
    EXECUTE 'REVOKE SELECT ON audit_entries FROM gocell_audit_admin';
  END IF;
END $$;
-- +goose StatementEnd
