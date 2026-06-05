//go:build integration

package postgres

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// PR-3a (#1341) FORCE ROW LEVEL SECURITY integration tests. These exercise the
// real DB-kernel isolation: a restricted (NON-superuser, NON-owner, NOBYPASSRLS)
// role + TxManager.RunInTx's SET LOCAL app.tenant_id injection against the config
// tables that migration 052 placed under RLS. The package's other integration
// tests connect as the container superuser, which bypasses RLS entirely — so RLS
// can ONLY be observed through a restricted role like the one built here (spec
// F-B11). Not parallel: it provisions a cluster-global role.

const (
	rlsTenantA = tenant.TenantID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	rlsTenantB = tenant.TenantID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	rlsAppRole = "gocell_rls_app"
	rlsAppPass = "rls_app_pw"
)

// swapUserInDSN returns dsn with its userinfo replaced. testcontainers emits the
// canonical URL form scheme://user:pass@host:port/db?query, so net/url suffices.
func swapUserInDSN(t *testing.T, dsn, user, pass string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err, "parse DSN")
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// restrictedAppPool provisions (via the admin/superuser pool on the same DB) a
// NOSUPERUSER NOBYPASSRLS non-owner role with DML grants and returns a Pool
// connected AS that role. FORCE ROW LEVEL SECURITY constrains the owner; a
// superuser/BYPASSRLS role bypasses RLS, so a restricted role is required to
// observe isolation.
func restrictedAppPool(t *testing.T, dsn string, admin *Pool) *Pool {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`DO $$ BEGIN
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + rlsAppRole + `') THEN
		     CREATE ROLE ` + rlsAppRole + ` LOGIN PASSWORD '` + rlsAppPass + `' NOSUPERUSER NOBYPASSRLS;
		   END IF;
		 END $$;`,
		`GRANT USAGE ON SCHEMA public TO ` + rlsAppRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + rlsAppRole,
	}
	for _, s := range stmts {
		if _, err := admin.DB().Exec(ctx, s); err != nil {
			t.Fatalf("provision restricted role: %v\nstmt: %s", err, s)
		}
	}
	appPool, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, rlsAppRole, rlsAppPass)})
	require.NoError(t, err, "open restricted-role pool")
	t.Cleanup(func() { _ = appPool.Close(ctx) })
	return appPool
}

// scopedInsertFlag inserts a feature_flags row under tenant tid's RLS scope.
func scopedInsertFlag(t *testing.T, tm *TxManager, tid tenant.TenantID, id, key string) error {
	t.Helper()
	return tm.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
		require.True(t, ok, "ambient tx must be present")
		_, err := tx.Exec(ctx, `INSERT INTO feature_flags (id, tenant_id, key) VALUES ($1, $2, $3)`, id, string(tid), key)
		return err
	})
}

// scopedCountFlags counts feature_flags rows with the given key VISIBLE under
// tenant tid's RLS scope. The WHERE clause deliberately omits tenant_id — RLS
// supplies the tenant predicate, so this measures DB-enforced isolation.
func scopedCountFlags(t *testing.T, tm *TxManager, tid tenant.TenantID, key string) int {
	t.Helper()
	var n int
	err := tm.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
		require.True(t, ok, "ambient tx must be present")
		return tx.QueryRow(ctx, `SELECT count(*) FROM feature_flags WHERE key = $1`, key).Scan(&n)
	})
	require.NoError(t, err)
	return n
}

func TestRLSForce_CrossTenantIsolation(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	require.NoError(t, scopedInsertFlag(t, tm, rlsTenantA, "flag-iso-a", "k-iso"))

	assert.Equal(t, 1, scopedCountFlags(t, tm, rlsTenantA, "k-iso"),
		"tenant A must see its own row")
	assert.Equal(t, 0, scopedCountFlags(t, tm, rlsTenantB, "k-iso"),
		"tenant B must NOT see tenant A's row (RLS USING isolation)")
}

func TestRLSForce_InsertWithCheckRejectsCrossTenant(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	// Scope = A, but the row claims tenant_id = B → WITH CHECK must reject.
	err := tm.RunInTx(tenant.WithScope(context.Background(), rlsTenantA), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		_, e := tx.Exec(ctx, `INSERT INTO feature_flags (id, tenant_id, key) VALUES ($1, $2, $3)`,
			"flag-wc", string(rlsTenantB), "k-wc")
		return e
	})
	require.Error(t, err, "writing a row for another tenant must be rejected by WITH CHECK")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42501", pgErr.Code, "expected row-level-security WITH CHECK violation (SQLSTATE 42501)")
}

func TestRLSForce_PoolNoTenantLeak(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)
	ctx := context.Background()

	// Seed under tenant A inside a scoped tx.
	require.NoError(t, scopedInsertFlag(t, tm, rlsTenantA, "flag-leak", "k-leak"))

	// A BARE pool query (no scope, no tx → no SET LOCAL) must see 0 rows: the GUC
	// is unset, NULLIF(...)→NULL, fail-closed. SET LOCAL from the prior tx was
	// auto-reset on commit, so nothing leaks onto the reused connection.
	var n int
	require.NoError(t, app.DB().QueryRow(ctx, `SELECT count(*) FROM feature_flags WHERE key = $1`, "k-leak").Scan(&n))
	assert.Equal(t, 0, n, "non-scoped pool read must be fail-closed (0 rows)")

	// And the GUC is empty on a freshly acquired connection (no residue).
	var setting string
	require.NoError(t, app.DB().QueryRow(ctx, `SELECT COALESCE(current_setting('app.tenant_id', true), '')`).Scan(&setting))
	assert.Equal(t, "", setting, "app.tenant_id must not leak across pooled connections")
}

func TestRLSForce_AppRoleNotBypassRLS(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	ctx := context.Background()

	var bypass, super bool
	require.NoError(t, app.DB().QueryRow(ctx,
		`SELECT rolbypassrls, rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&bypass, &super))
	assert.False(t, bypass, "[F-B11] application role must NOT have BYPASSRLS")
	assert.False(t, super, "[F-B11] application role must NOT be a superuser")
}

func TestRLSForce_SystemTenantStrictEquality(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	require.NoError(t, scopedInsertFlag(t, tm, tenant.SystemTenantID, "flag-sys", "k-sys"))

	assert.Equal(t, 1, scopedCountFlags(t, tm, tenant.SystemTenantID, "k-sys"),
		"SystemTenantID scope must see the system-tier row")
	assert.Equal(t, 0, scopedCountFlags(t, tm, rlsTenantA, "k-sys"),
		"a real tenant must NOT see SystemTenantID rows (strict equality, no OR-merge)")
}

func TestRLSForce_SchemaGuardVerifyRLS(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)

	// Positive: after migration 052 the config tables are FORCE RLS + policy.
	require.NoError(t, VerifyExpectedShape(ctx, pool),
		"VerifyExpectedShape must pass with RLS enabled on the config tables")

	// Negative: drop one policy → verifyRLS dimension fails.
	_, err := pool.DB().Exec(ctx, `DROP POLICY tenant_isolation ON feature_flags`)
	require.NoError(t, err)
	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must fail once the tenant_isolation policy is dropped")
}
