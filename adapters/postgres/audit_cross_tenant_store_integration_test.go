//go:build integration

package postgres

// TestAuditCrossTenantStore_PG is an integration test for AuditCrossTenantStore
// against a real PostgreSQL database using the gocell_audit_admin role.
//
// REQUIRES LIVE PG: this test requires a PostgreSQL instance with the
// gocell_audit_admin role provisioned and a permissive SELECT policy
// USING(true) on audit_entries. It is gated behind the `integration` build tag.
//
// The gocell_audit_admin role must:
//   - Have FORCE RLS active (FORCE ROW LEVEL SECURITY).
//   - Have a permissive SELECT policy USING(true) (reads across all tenants).
//   - NOT have INSERT/UPDATE/DELETE (read-only).
//
// Without the gocell_audit_admin role the test is structurally skipped — the
// migratedPool is provisioned as superuser; provisioning the admin role and its
// permissive policy is a separate migration step (#1810). This test documents
// that dependency and validates the query shape using the
// crossTenantSQLForTest+crossTenantSQLHasNoTenantPredicate helpers that do NOT
// require a real connection (see audit_cross_tenant_store_test.go for those).
//
// The SQL-shape unit tests in audit_cross_tenant_store_test.go run without PG.
