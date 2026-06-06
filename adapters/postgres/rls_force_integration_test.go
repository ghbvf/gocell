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
	"github.com/ghbvf/gocell/pkg/errcode"
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
		// Only (id, tenant_id, key) are supplied; the remaining NOT NULL columns
		// rely on their migration-051 DEFAULTs (enabled=false, rollout_percentage=0,
		// description='', version=1, created_at/updated_at=now()). These RLS tests
		// assert isolation, not schema completeness — if a future migration drops a
		// default this INSERT fails NOT NULL (not RLS), which is the intended loud
		// signal to revisit this helper.
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

// TestRLSForce_AppRoleRestrictedProbe exercises the runtime readyz precondition
// probe (#1676, postgres_app_role_restricted_ready): the restricted serving role
// must PASS (RLS effective), while the admin/superuser pool — which bypasses RLS —
// must FAIL with ErrAdapterPGRoleBypassRLS. This is the production wiring of the
// rolbypassrls=false assertion the suite has proved structurally above; corebundle
// registers it via Config.RequireRestrictedRole so a superuser-served deployment
// reports /readyz 503 (RLS is a runtime no-op there).
func TestRLSForce_AppRoleRestrictedProbe(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	ctx := context.Background()

	require.NoError(t, app.AppRoleRestrictedCheck(ctx),
		"[F-B11] restricted (NOSUPERUSER NOBYPASSRLS) serving role must pass the precondition probe")

	err := admin.AppRoleRestrictedCheck(ctx)
	require.Error(t, err,
		"[F-B11] superuser/BYPASSRLS role must fail the precondition probe (RLS not enforced at runtime)")
	var coded *errcode.Error
	require.ErrorAs(t, err, &coded, "probe failure must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGRoleBypassRLS, coded.Code)
}

// TestRLSForce_TenantStrictEquality asserts that RLS enforces strict per-tenant
// equality: rows seeded under rlsTenantA are invisible to rlsTenantB. This
// replaces the previous SystemTenantID-based test; the nil-UUID sentinel was
// removed in issue #1577 — every tenant must be a real non-reserved UUID.
func TestRLSForce_TenantStrictEquality(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	require.NoError(t, scopedInsertFlag(t, tm, rlsTenantA, "flag-teq", "k-teq"))

	assert.Equal(t, 1, scopedCountFlags(t, tm, rlsTenantA, "k-teq"),
		"tenant A scope must see its own row")
	assert.Equal(t, 0, scopedCountFlags(t, tm, rlsTenantB, "k-teq"),
		"tenant B must NOT see tenant A's row (strict equality, no OR-merge)")
}

func TestRLSForce_SchemaGuardVerifyRLS(t *testing.T) {
	ctx := context.Background()

	// Positive: after migrations 052 (config) + 053 (accesscore) all six tenant
	// tables are FORCE RLS + policy.
	require.NoError(t, VerifyExpectedShape(ctx, migratedPool(t)),
		"VerifyExpectedShape must pass with RLS enabled on all six tenant tables")

	// Negative: dropping the tenant_isolation policy on ANY of the six RLS tables
	// must make verifyRLS fail — each on its own fresh clone so the drops don't
	// interfere (proves verifyRLS checks every table, not just the first).
	for _, table := range []string{
		"config_entries", "config_versions", "feature_flags",
		"users", "roles", "role_assignments",
	} {
		t.Run("drop_policy_"+table, func(t *testing.T) {
			pool := migratedPool(t)
			_, err := pool.DB().Exec(ctx, `DROP POLICY tenant_isolation ON `+table)
			require.NoError(t, err)
			require.Error(t, VerifyExpectedShape(ctx, pool),
				"VerifyExpectedShape must fail once tenant_isolation is dropped on "+table)
		})
	}

	// Negative (#1622 F1): a policy that EXISTS BY NAME but is semantically
	// weakened must still fail verifyRLSPolicy — name presence alone is not the
	// security property. Each case rebuilds tenant_isolation on feature_flags (a
	// fresh clone per sub-test) with a specific defect class.
	const okUsing = `(tenant_id = NULLIF(current_setting('app.tenant_id', true), ''))`
	semanticDefects := []struct {
		name  string
		setup string // SQL run after a fresh migratedPool, replacing/adding policy on feature_flags
	}{
		{
			// USING(true): the tenant filter is gone — every tenant's rows are visible.
			name: "using_true",
			setup: `DROP POLICY tenant_isolation ON feature_flags;
			        CREATE POLICY tenant_isolation ON feature_flags
			          USING (true) WITH CHECK (true)`,
		},
		{
			// Missing WITH CHECK: reads are scoped but an INSERT can stamp another
			// tenant's id (the write-side guard is gone).
			name: "missing_with_check",
			setup: `DROP POLICY tenant_isolation ON feature_flags;
			        CREATE POLICY tenant_isolation ON feature_flags USING ` + okUsing,
		},
		{
			// Extra permissive policy: OR-ed into the USING filter, re-widening rows.
			name:  "extra_permissive_policy",
			setup: `CREATE POLICY extra_visible ON feature_flags FOR SELECT USING (true)`,
		},
		{
			// #1622 F1 round-2 — WRONG column: still reads app.tenant_id (a substring
			// check passed it) but isolates on `id`, not tenant_id. Validates the
			// whole-predicate regex against real pg_get_expr deparse.
			name: "using_wrong_column",
			setup: `DROP POLICY tenant_isolation ON feature_flags;
			        CREATE POLICY tenant_isolation ON feature_flags
			          USING (id = NULLIF(current_setting('app.tenant_id', true), ''))
			          WITH CHECK (id = NULLIF(current_setting('app.tenant_id', true), ''))`,
		},
		{
			// #1622 F1 round-2 — VACUOUS `… OR true`: mentions app.tenant_id but is
			// always true. The anchored ^…$ regex must reject the real-pg-rendered OR.
			name: "using_or_true",
			setup: `DROP POLICY tenant_isolation ON feature_flags;
			        CREATE POLICY tenant_isolation ON feature_flags
			          USING (` + okUsing + ` OR true)
			          WITH CHECK ` + okUsing,
		},
	}
	for _, dc := range semanticDefects {
		t.Run("semantic_"+dc.name, func(t *testing.T) {
			pool := migratedPool(t)
			_, err := pool.DB().Exec(ctx, dc.setup)
			require.NoError(t, err)
			require.Error(t, VerifyExpectedShape(ctx, pool),
				"VerifyExpectedShape must fail for a semantically weakened tenant_isolation policy ("+dc.name+")")
		})
	}
}

// ── PR-3b (#1617) accesscore-table RLS isolation ─────────────────────────────
//
// These mirror the config-table tests above against the accesscore tables that
// migration 053 placed under FORCE RLS (users/roles/role_assignments), exercised
// through the restricted (NON-superuser, NOBYPASSRLS) role so RLS is observable.

// scopedInsertUser inserts a minimal valid users row under tenant tid's RLS scope.
// Only the NOT NULL columns without a DEFAULT are supplied (migration 050); the
// rest rely on their column defaults.
func scopedInsertUser(t *testing.T, tm *TxManager, tid tenant.TenantID, id, username string) error {
	t.Helper()
	return tm.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		tx, ok := persistence.TxFromContext[pgx.Tx](ctx)
		require.True(t, ok, "ambient tx must be present")
		// WHERE-less INSERT: RLS WITH CHECK validates tenant_id against app.tenant_id.
		_, err := tx.Exec(ctx, `INSERT INTO users
			(id, tenant_id, username, email, password_hash, status, creation_source, authz_epoch, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'h', 'active', 'identity', 1, now(), now())`,
			id, string(tid), username, username+"@rls.local")
		return err
	})
}

// scopedCountUsers counts users rows with the given username VISIBLE under tenant
// tid's RLS scope. The WHERE clause omits tenant_id — RLS supplies the predicate.
func scopedCountUsers(t *testing.T, tm *TxManager, tid tenant.TenantID, username string) int {
	t.Helper()
	var n int
	require.NoError(t, tm.RunInTx(tenant.WithScope(context.Background(), tid), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		return tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE username = $1`, username).Scan(&n)
	}))
	return n
}

func TestRLSForce_Accesscore_CrossTenantIsolation(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	require.NoError(t, scopedInsertUser(t, tm, rlsTenantA, "11111111-1111-1111-1111-111111111111", "u-iso"))

	assert.Equal(t, 1, scopedCountUsers(t, tm, rlsTenantA, "u-iso"),
		"tenant A must see its own users row")
	assert.Equal(t, 0, scopedCountUsers(t, tm, rlsTenantB, "u-iso"),
		"tenant B must NOT see tenant A's users row (RLS USING isolation)")
}

func TestRLSForce_Accesscore_InsertWithCheckRejectsCrossTenant(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	// Scope = A, but the row claims tenant_id = B → WITH CHECK must reject.
	err := tm.RunInTx(tenant.WithScope(context.Background(), rlsTenantA), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		_, e := tx.Exec(ctx, `INSERT INTO users
			(id, tenant_id, username, email, password_hash, status, creation_source, authz_epoch, created_at, updated_at)
			VALUES ($1, $2, 'u-wc', 'u-wc@rls.local', 'h', 'active', 'identity', 1, now(), now())`,
			"22222222-2222-2222-2222-222222222222", string(rlsTenantB))
		return e
	})
	require.Error(t, err, "writing a users row for another tenant must be rejected by WITH CHECK")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42501", pgErr.Code, "expected row-level-security WITH CHECK violation (SQLSTATE 42501)")
}

func TestRLSForce_Accesscore_RoleAssignmentSameTenantFK(t *testing.T) {
	dsn := sharedPG.CloneDSN(t)
	admin := openPerTestPool(t, dsn)
	app := restrictedAppPool(t, dsn, admin)
	tm := NewTxManager(app)

	const userID = "33333333-3333-3333-3333-333333333333"
	require.NoError(t, scopedInsertUser(t, tm, rlsTenantA, userID, "u-fk"))
	// Seed a role + a same-tenant assignment under A — must succeed.
	require.NoError(t, tm.RunInTx(tenant.WithScope(context.Background(), rlsTenantA), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		if _, e := tx.Exec(ctx, `INSERT INTO roles (tenant_id, id, name) VALUES ($1, 'viewer', 'Viewer')`, string(rlsTenantA)); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO role_assignments (tenant_id, user_id, role_id) VALUES ($1, $2, 'viewer')`, string(rlsTenantA), userID)
		return e
	}), "same-tenant role assignment must succeed")

	// Cross-tenant grant: scope = B, assign B-tenant grant to A's user → the
	// composite FK (tenant_id, user_id) -> users(tenant_id, id) finds no such user
	// in tenant B (RLS-invisible + no row), so the FK is violated (23503).
	err := tm.RunInTx(tenant.WithScope(context.Background(), rlsTenantB), func(ctx context.Context) error {
		tx, _ := persistence.TxFromContext[pgx.Tx](ctx)
		if _, e := tx.Exec(ctx, `INSERT INTO roles (tenant_id, id, name) VALUES ($1, 'viewer', 'Viewer')`, string(rlsTenantB)); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO role_assignments (tenant_id, user_id, role_id) VALUES ($1, $2, 'viewer')`, string(rlsTenantB), userID)
		return e
	})
	require.Error(t, err, "granting a role to a user that does not exist in this tenant must be rejected")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	// 23503 = foreign_key_violation; 42501 = RLS WITH CHECK — either is a valid
	// cross-tenant rejection (the composite FK and RLS both fail-close here).
	assert.Contains(t, []string{"23503", "42501"}, pgErr.Code,
		"expected FK (23503) or RLS (42501) rejection of the cross-tenant grant, got "+pgErr.Code)
}
