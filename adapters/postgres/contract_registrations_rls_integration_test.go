//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContractRegistrations_RLS_TenantIsolation_ServingRole proves the migration-066
// FORCE ROW LEVEL SECURITY + tenant_isolation policy actually isolates at runtime —
// not just that the policy shape exists (that is schema_guard's static job). It runs
// AS the restricted gocell_app serving role (NOSUPERUSER NOBYPASSRLS), where RLS is
// effective, unlike the superuser/owner pool used by the cell repo integration tests.
//
// This is the read-side complement to the #2236 review finding F1: a tenant-scoped
// read MUST run with the app.tenant_id GUC set (a tenant-scoped tx) — an UNSCOPED
// read fail-closes to 0 rows under FORCE RLS, and an unscoped INSERT is rejected by
// WITH CHECK. (The production read-scoping funnel — wrapping repo reads in
// tenant.WithScope + RunInTx, à la configcore/internal/scopedread — is wired in US6;
// this test pins the DB-engine guarantee the funnel rests on.)
//
// Not parallel: provisions the cluster-global gocell_app role.
func TestContractRegistrations_RLS_TenantIsolation_ServingRole(t *testing.T) {
	ctx := context.Background()
	const (
		tenantA = "00000000-0000-0000-0000-000000000001"
		tenantB = "00000000-0000-0000-0000-000000000002"
	)

	dsn := sharedPG.EmptyDSN(t)
	owner := openPerTestPool(t, dsn)
	provisionServingRole(t, ctx, owner)

	fsys, err := MigrationsFS()
	require.NoError(t, err)
	migrator, err := newMigratorForTable(owner, fsys, "schema_migrations")
	require.NoError(t, err)
	defer func() { _ = migrator.Close() }()
	require.NoError(t, migrator.Up(ctx), "migrate up")

	app, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, servingRoleName, servingRolePassword)})
	require.NoError(t, err, "open serving-role pool")
	defer func() { _ = app.Close(ctx) }()

	// Scoped write under tenant A: SET LOCAL app.tenant_id satisfies WITH CHECK.
	txA, err := app.DB().Begin(ctx)
	require.NoError(t, err)
	_, err = txA.Exec(ctx, `SET LOCAL app.tenant_id = '`+tenantA+`'`)
	require.NoError(t, err)
	_, err = txA.Exec(ctx,
		`INSERT INTO contract_registrations (tenant_id, id, kind, submitter, state, created_at, updated_at)
		 VALUES ($1, 'r1', 'http', 'alice', 'submitted', now(), now())`, tenantA)
	require.NoError(t, err, "scoped INSERT under own tenant must satisfy WITH CHECK")
	var scopedCount int
	require.NoError(t, txA.QueryRow(ctx, `SELECT count(*) FROM contract_registrations`).Scan(&scopedCount))
	assert.Equal(t, 1, scopedCount, "scoped read under own tenant sees the row")
	require.NoError(t, txA.Commit(ctx))

	// Unscoped read (no GUC) → FORCE RLS fail-closes to 0 rows (USING predicate
	// evaluates tenant_id = NULL). This is exactly the F1 hazard: a bare-pool read
	// returns empty under the restricted role.
	var unscopedCount int
	require.NoError(t, app.DB().QueryRow(ctx, `SELECT count(*) FROM contract_registrations`).Scan(&unscopedCount))
	assert.Equal(t, 0, unscopedCount, "unscoped read (GUC unset) must fail-close to 0 rows under FORCE RLS")

	// Cross-tenant scoped read (tenant B) → 0 rows (tenant_isolation).
	txB, err := app.DB().Begin(ctx)
	require.NoError(t, err)
	_, err = txB.Exec(ctx, `SET LOCAL app.tenant_id = '`+tenantB+`'`)
	require.NoError(t, err)
	var crossCount int
	require.NoError(t, txB.QueryRow(ctx, `SELECT count(*) FROM contract_registrations`).Scan(&crossCount))
	assert.Equal(t, 0, crossCount, "tenant B must not see tenant A's registration")
	require.NoError(t, txB.Rollback(ctx))

	// Unscoped INSERT → rejected by WITH CHECK (tenant_id = NULL).
	_, err = app.DB().Exec(ctx,
		`INSERT INTO contract_registrations (tenant_id, id, kind, submitter, state, created_at, updated_at)
		 VALUES ($1, 'r2', 'http', 'bob', 'submitted', now(), now())`, tenantA)
	require.Error(t, err, "unscoped INSERT (GUC unset) must be rejected by FORCE RLS WITH CHECK")
}
