//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectionEvents_AppendOnly_ServingRoleRevoked is the regression guard for
// the #1504 D7/I4 append-only invariant: the serving role must never be able to
// UPDATE or DELETE the durable projection_events journal. The guard itself is the
// DB-engine REVOKE in migration 058 (unbypassable, unlike the I4 archtest which has
// dynamic-SQL/cross-adapter blind spots); this test proves the REVOKE actually
// fires and survives a re-grant of the deploy default privileges.
//
// It faithfully reproduces the deploy ordering (deploy/postgres/init/10-restricted-
// role.sh runs BEFORE migrations): create gocell_app, grant it the default
// SELECT/INSERT/UPDATE/DELETE on future tables, THEN run the migration set. After
// migration the serving role must retain only SELECT (read for replay/Position) +
// INSERT (the same-transaction journaling writer), with UPDATE/DELETE revoked.
//
// Not parallel: it provisions the cluster-global gocell_app role + per-DB default
// privileges.
func TestProjectionEvents_AppendOnly_ServingRoleRevoked(t *testing.T) {
	ctx := context.Background()

	// Fresh empty DB; the pool connects as the owning/migrating superuser.
	dsn := sharedPG.EmptyDSN(t)
	owner := openPerTestPool(t, dsn)

	// Provision the shared serving role + default DML grant BEFORE migrations, so
	// projection_events is born with UPDATE/DELETE that migration 058's REVOKE removes.
	provisionServingRole(t, ctx, owner)

	// Run the full platform migration set (058 creates projection_events and, with
	// gocell_app present, revokes UPDATE/DELETE).
	fsys, err := MigrationsFS()
	require.NoError(t, err)
	migrator, err := newMigratorForTable(owner, fsys, "schema_migrations")
	require.NoError(t, err)
	defer func() { _ = migrator.Close() }()
	require.NoError(t, migrator.Up(ctx), "migrate up")

	// Static assertion: the grant catalog reflects append-only for the serving role.
	var canSelect, canInsert, canUpdate, canDelete bool
	require.NoError(t, owner.DB().QueryRow(ctx,
		`SELECT has_table_privilege($1, 'projection_events', 'SELECT'),
		        has_table_privilege($1, 'projection_events', 'INSERT'),
		        has_table_privilege($1, 'projection_events', 'UPDATE'),
		        has_table_privilege($1, 'projection_events', 'DELETE')`,
		servingRoleName).Scan(&canSelect, &canInsert, &canUpdate, &canDelete))
	assert.True(t, canSelect, "serving role must keep SELECT (replay/Position read)")
	assert.True(t, canInsert, "serving role must keep INSERT (same-tx journaling writer)")
	assert.False(t, canUpdate, "append-only: serving role UPDATE must be revoked (058)")
	assert.False(t, canDelete, "append-only: serving role DELETE must be revoked (058)")

	// Behavioral assertion: connect AS gocell_app and prove it at the wire — INSERT
	// succeeds, UPDATE/DELETE are denied by the engine (42501 insufficient_privilege).
	app, err := NewPool(ctx, Config{DSN: swapUserInDSN(t, dsn, servingRoleName, servingRolePassword)})
	require.NoError(t, err, "open serving-role pool")
	defer func() { _ = app.Close(ctx) }()

	_, err = app.DB().Exec(ctx,
		`INSERT INTO projection_events (id, event_type, payload, created_at, occurred_at)
		 VALUES ('evt-appendonly-1', 'test.event', '{}'::jsonb, now(), now())`)
	require.NoError(t, err, "serving role must be able to append (INSERT)")

	_, err = app.DB().Exec(ctx, `UPDATE projection_events SET event_type = 'mutated' WHERE id = 'evt-appendonly-1'`)
	assertInsufficientPrivilege(t, err, "UPDATE")

	_, err = app.DB().Exec(ctx, `DELETE FROM projection_events WHERE id = 'evt-appendonly-1'`)
	assertInsufficientPrivilege(t, err, "DELETE")
}

// assertInsufficientPrivilege asserts err is a PG permission-denied error (SQLSTATE
// 42501), i.e. the DB engine — not application code — blocked the mutation.
func assertInsufficientPrivilege(t *testing.T, err error, op string) {
	t.Helper()
	require.Errorf(t, err, "%s on the append-only journal must be denied", op)
	var pgErr *pgconn.PgError
	require.ErrorAsf(t, err, &pgErr, "%s denial must be a *pgconn.PgError (got %v)", op, err)
	assert.Equalf(t, "42501", pgErr.Code,
		"%s must fail with insufficient_privilege (42501), got SQLSTATE %s", op, pgErr.Code)
}
