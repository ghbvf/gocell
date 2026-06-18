//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContractRegistrationEvents_AppendOnly_ServingRoleRevoked is the regression
// guard for the 303-US5 (#2236) append-only migration-history invariant: the
// serving role must never UPDATE or DELETE contract_registration_events. The
// guard is the DB-engine REVOKE in migration 066 (unbypassable); this test proves
// the REVOKE fires after a re-grant of the deploy default privileges, and that the
// projection table contract_registrations stays mutable (state transitions UPDATE
// it). Mirrors TestProjectionEvents_AppendOnly_ServingRoleRevoked (migration 058).
//
// Not parallel: it provisions the cluster-global gocell_app role + per-DB default
// privileges.
func TestContractRegistrationEvents_AppendOnly_ServingRoleRevoked(t *testing.T) {
	ctx := context.Background()
	const (
		appRole = "gocell_app"
		// gocell_app is a CLUSTER-GLOBAL role shared with the other *_appendonly
		// tests (e.g. projection_events). CREATE ROLE ... IF NOT EXISTS does NOT
		// update an existing role's password, so all such tests MUST use the same
		// password or whichever runs second fails SASL auth (28P01). Keep in sync.
		appPass = "projevents_appkey"
	)

	dsn := sharedPG.EmptyDSN(t)
	owner := openPerTestPool(t, dsn)

	// Reproduce 10-restricted-role.sh ordering: role + default DML grant BEFORE
	// migrations, so the history table is born with UPDATE/DELETE for gocell_app
	// and migration 066's REVOKE has something to remove.
	provision := []string{
		`DO $$ BEGIN
		   IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + appRole + `') THEN
		     CREATE ROLE ` + appRole + ` LOGIN PASSWORD '` + appPass + `' NOSUPERUSER NOBYPASSRLS;
		   END IF;
		 END $$;`,
		`GRANT USAGE ON SCHEMA public TO ` + appRole,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ` + appRole,
	}
	for _, s := range provision {
		_, err := owner.DB().Exec(ctx, s)
		require.NoErrorf(t, err, "provision serving role\nstmt: %s", s)
	}

	fsys, err := MigrationsFS()
	require.NoError(t, err)
	migrator, err := newMigratorForTable(owner, fsys, "schema_migrations")
	require.NoError(t, err)
	defer func() { _ = migrator.Close() }()
	require.NoError(t, migrator.Up(ctx), "migrate up")

	// Static assertion: append-only on the history table; mutable projection.
	var evSelect, evInsert, evUpdate, evDelete, evTruncate, projUpdate bool
	require.NoError(t, owner.DB().QueryRow(ctx,
		`SELECT has_table_privilege($1, 'contract_registration_events', 'SELECT'),
		        has_table_privilege($1, 'contract_registration_events', 'INSERT'),
		        has_table_privilege($1, 'contract_registration_events', 'UPDATE'),
		        has_table_privilege($1, 'contract_registration_events', 'DELETE'),
		        has_table_privilege($1, 'contract_registration_events', 'TRUNCATE'),
		        has_table_privilege($1, 'contract_registrations', 'UPDATE')`,
		appRole).Scan(&evSelect, &evInsert, &evUpdate, &evDelete, &evTruncate, &projUpdate))
	assert.True(t, evSelect, "serving role must keep SELECT on the history (replay/read)")
	assert.True(t, evInsert, "serving role must keep INSERT on the history (append)")
	assert.False(t, evUpdate, "append-only: serving role UPDATE on history must be revoked (066)")
	assert.False(t, evDelete, "append-only: serving role DELETE on history must be revoked (066)")
	assert.False(t, evTruncate, "append-only: serving role must never hold TRUNCATE on history")
	assert.True(t, projUpdate, "projection contract_registrations must stay mutable (state transitions UPDATE it)")

	// Behavioral assertion: connect AS gocell_app (NOSUPERUSER NOBYPASSRLS) and
	// prove append-only at the wire.
	app, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, appRole, appPass)})
	require.NoError(t, err, "open serving-role pool")
	defer func() { _ = app.Close(ctx) }()

	const tenantA = "00000000-0000-0000-0000-000000000001"
	// The INSERT must satisfy FORCE ROW LEVEL SECURITY WITH CHECK (tenant_isolation),
	// so set the tenant GUC for the inserting transaction (SET LOCAL is tx-scoped).
	// Unlike projection_events (no RLS), this table is tenant-scoped — a bare INSERT
	// with an unset GUC is correctly rejected by RLS.
	appTx, err := app.DB().Begin(ctx)
	require.NoError(t, err)
	_, err = appTx.Exec(ctx, `SET LOCAL app.tenant_id = '`+tenantA+`'`)
	require.NoError(t, err)
	_, err = appTx.Exec(ctx,
		`INSERT INTO contract_registration_events
		   (tenant_id, registration_id, seq, from_state, to_state, actor, reason, occurred_at)
		 VALUES ($1, 'evt-ao-1', 1, '', 'submitted', 'alice', '', now())`, tenantA)
	require.NoError(t, err, "serving role must be able to append (INSERT) within its tenant scope")
	require.NoError(t, appTx.Commit(ctx))

	// UPDATE / DELETE / TRUNCATE are denied at the privilege layer (SQLSTATE 42501,
	// evaluated before RLS, so no GUC needed): UPDATE/DELETE by migration-066 REVOKE,
	// TRUNCATE because it is never default-granted to the serving role.
	_, err = app.DB().Exec(ctx, `UPDATE contract_registration_events SET actor = 'mutated' WHERE registration_id = 'evt-ao-1'`)
	assertInsufficientPrivilege(t, err, "UPDATE")

	_, err = app.DB().Exec(ctx, `DELETE FROM contract_registration_events WHERE registration_id = 'evt-ao-1'`)
	assertInsufficientPrivilege(t, err, "DELETE")

	_, err = app.DB().Exec(ctx, `TRUNCATE contract_registration_events`)
	assertInsufficientPrivilege(t, err, "TRUNCATE")
}
