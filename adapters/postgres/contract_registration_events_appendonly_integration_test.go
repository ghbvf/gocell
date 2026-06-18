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
		appPass = "contractreg_appkey"
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
	var evSelect, evInsert, evUpdate, evDelete, projUpdate bool
	require.NoError(t, owner.DB().QueryRow(ctx,
		`SELECT has_table_privilege($1, 'contract_registration_events', 'SELECT'),
		        has_table_privilege($1, 'contract_registration_events', 'INSERT'),
		        has_table_privilege($1, 'contract_registration_events', 'UPDATE'),
		        has_table_privilege($1, 'contract_registration_events', 'DELETE'),
		        has_table_privilege($1, 'contract_registrations', 'UPDATE')`,
		appRole).Scan(&evSelect, &evInsert, &evUpdate, &evDelete, &projUpdate))
	assert.True(t, evSelect, "serving role must keep SELECT on the history (replay/read)")
	assert.True(t, evInsert, "serving role must keep INSERT on the history (append)")
	assert.False(t, evUpdate, "append-only: serving role UPDATE on history must be revoked (066)")
	assert.False(t, evDelete, "append-only: serving role DELETE on history must be revoked (066)")
	assert.True(t, projUpdate, "projection contract_registrations must stay mutable (state transitions UPDATE it)")

	// Behavioral assertion: connect AS gocell_app and prove it at the wire.
	app, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, appRole, appPass)})
	require.NoError(t, err, "open serving-role pool")
	defer func() { _ = app.Close(ctx) }()

	_, err = app.DB().Exec(ctx,
		`INSERT INTO contract_registration_events
		   (tenant_id, registration_id, seq, from_state, to_state, actor, reason, occurred_at)
		 VALUES ('00000000-0000-0000-0000-000000000001', 'evt-ao-1', 1, '', 'submitted', 'alice', '', now())`)
	require.NoError(t, err, "serving role must be able to append (INSERT)")

	_, err = app.DB().Exec(ctx, `UPDATE contract_registration_events SET actor = 'mutated' WHERE registration_id = 'evt-ao-1'`)
	assertInsufficientPrivilege(t, err, "UPDATE")

	_, err = app.DB().Exec(ctx, `DELETE FROM contract_registration_events WHERE registration_id = 'evt-ao-1'`)
	assertInsufficientPrivilege(t, err, "DELETE")
}
