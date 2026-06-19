//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSagaProjectionDeadLetters_InsertOnly_ServingRoleRevoked is the regression
// guard for the #2110 / #2419-F1 least-privilege invariant: the serving role must
// be able to INSERT poison entries (DeadLetterStore.Record) but must NOT be able to
// SELECT, UPDATE, or DELETE from saga_projection_dead_letters.
//
// This table differs from projection_events (058) which keeps SELECT for
// replay/Position reads. Here there is no serving-path read at all — ops triage and
// recovery reads are admin actions performed by a dedicated admin/read-only role,
// not the serving role. The DB-engine REVOKE SELECT, UPDATE, DELETE in migration 067
// is the primary (unbypassable) guard; this test proves the REVOKE fires and
// survives a re-grant of the deploy default privileges.
//
// It faithfully reproduces the deploy ordering
// (deploy/postgres/init/10-restricted-role.sh runs BEFORE migrations): create
// gocell_app, grant it the default SELECT/INSERT/UPDATE/DELETE on future tables,
// THEN run the migration set. After migration the serving role must retain only
// INSERT, with SELECT/UPDATE/DELETE revoked.
//
// Not parallel: it provisions the cluster-global gocell_app role + per-DB default
// privileges.
func TestSagaProjectionDeadLetters_InsertOnly_ServingRoleRevoked(t *testing.T) {
	ctx := context.Background()

	// Fresh empty DB; the pool connects as the owning/migrating superuser.
	dsn := sharedPG.EmptyDSN(t)
	owner := openPerTestPool(t, dsn)

	// Provision the shared serving role + default DML grant BEFORE migrations, so
	// saga_projection_dead_letters is born with SELECT/UPDATE/DELETE that migration
	// 067's REVOKE removes.
	provisionServingRole(t, ctx, owner)

	// Run the full platform migration set (067 creates saga_projection_dead_letters
	// and, with gocell_app present, revokes SELECT/UPDATE/DELETE).
	fsys, err := MigrationsFS()
	require.NoError(t, err)
	migrator, err := newMigratorForTable(owner, fsys, "schema_migrations")
	require.NoError(t, err)
	defer func() { _ = migrator.Close() }()
	require.NoError(t, migrator.Up(ctx), "migrate up")

	// Static assertion: the grant catalog reflects INSERT-only for the serving role.
	var canSelect, canInsert, canUpdate, canDelete bool
	require.NoError(t, owner.DB().QueryRow(ctx,
		`SELECT has_table_privilege($1, 'saga_projection_dead_letters', 'SELECT'),
		        has_table_privilege($1, 'saga_projection_dead_letters', 'INSERT'),
		        has_table_privilege($1, 'saga_projection_dead_letters', 'UPDATE'),
		        has_table_privilege($1, 'saga_projection_dead_letters', 'DELETE')`,
		servingRoleName).Scan(&canSelect, &canInsert, &canUpdate, &canDelete))
	assert.False(t, canSelect, "insert-only: serving role SELECT must be revoked (067) — no serving-path read")
	assert.True(t, canInsert, "serving role must keep INSERT (DeadLetterStore.Record)")
	assert.False(t, canUpdate, "insert-only: serving role UPDATE must be revoked (067)")
	assert.False(t, canDelete, "insert-only: serving role DELETE must be revoked (067)")

	// Behavioral assertion: connect AS gocell_app and prove it at the wire.
	// INSERT succeeds; SELECT/UPDATE/DELETE are denied by the engine (42501
	// insufficient_privilege). assertInsufficientPrivilege is defined in
	// projection_events_appendonly_integration_test.go (same package).
	app, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, servingRoleName, servingRolePassword)})
	require.NoError(t, err, "open serving-role pool")
	defer func() { _ = app.Close(ctx) }()

	_, err = app.DB().Exec(ctx,
		`INSERT INTO saga_projection_dead_letters
		   (cell_id, projection_id, global_seq, event_id, stream, error_type, error_message, occurred_at)
		 VALUES ('cell-test', 'proj-test', 1, 'evt-dl-1', 'test.stream', 'ERR_TEST', 'test error', $1)`,
		time.Now())
	require.NoError(t, err, "serving role must be able to append (INSERT)")

	_, err = app.DB().Exec(ctx, `SELECT 1 FROM saga_projection_dead_letters LIMIT 1`)
	assertInsufficientPrivilege(t, err, "SELECT")

	_, err = app.DB().Exec(ctx,
		`UPDATE saga_projection_dead_letters SET error_message = 'mutated' WHERE event_id = 'evt-dl-1'`)
	assertInsufficientPrivilege(t, err, "UPDATE")

	_, err = app.DB().Exec(ctx,
		`DELETE FROM saga_projection_dead_letters WHERE event_id = 'evt-dl-1'`)
	assertInsufficientPrivilege(t, err, "DELETE")
}
