//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/tests/testutil"
)

// setupPostgresForMain starts a PostgreSQL container and returns the DSN
// and a cleanup function. Migrations are applied by the caller when needed.
func setupPostgresForMain(t *testing.T) (string, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("testmain"),
		tcpostgres.WithUsername("testmain"),
		tcpostgres.WithPassword("testmain"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "failed to get connection string")

	cleanup := func() {
		if terr := container.Terminate(ctx); terr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", terr)
		}
	}
	return dsn, cleanup
}

// TestBuildConfigCoreOpts_Postgres_SchemaMatched verifies that provisionCapabilities
// succeeds and the full composition.Builder.Build succeeds when a real
// PostgreSQL container is available and all migrations have been applied.
//
// Pool provisioning is now handled by provisionCapabilities; this test calls
// LoadSharedDepsFromEnv, then provisionCapabilities, then composition.Builder.Build
// to assert the full postgres wiring path (equivalent to the old BuildApp path).
func TestBuildConfigCoreOpts_Postgres_SchemaMatched(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-apply all migrations so schema version matches the binary.
	migPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(migPool, testAdapterMigrationsFS(t), migration.PlatformNamespace)
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")
	_ = migPool.Close(ctx)

	// Set minimal env vars for postgres mode.
	setRealModeEnv(t, dsn)

	shared, locals, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	// provisionCapabilities opens the pool + verifies schema.
	require.NoError(t, provisionCapabilities(ctx, shared, locals),
		"provisionCapabilities must succeed with a fully migrated DB")
	require.NotNil(t, shared.PG, "shared.PG must be provisioned in postgres mode")
	defer func() {
		for _, p := range locals.poolMRs {
			_ = p.Close(ctx)
		}
	}()

	// composition.Builder.Build verifies that platform modules can Provide
	// with the PG capability. Bootstrap options (listeners, auth) are omitted
	// in this unit-level wiring test.
	mods := generatedCellModules()
	app, buildErr := composition.New(corebundleCellIDs()...).With(mods...).Build(ctx, shared,
		func(_ []cell.Cell) ([]bootstrap.Option, error) { return nil, nil })
	require.NoError(t, buildErr, "Builder.Build must succeed with a fully migrated DB")
	assert.NotNil(t, app, "App must be non-nil after successful Build")
}

// TestBuildConfigCoreOpts_Postgres_SchemaMismatch verifies that the schema guard
// fires when the DB schema version does not match the binary.
//
// Schema verification moved into percellpg.Resolve (invoked by
// provisionCapabilities) in #1964. This test drives the full
// env→provisionCapabilities path on a lagged DB and asserts the guard error
// surfaces fail-closed (percellpg owns the pool-open → verify sequence now, so
// the guard can no longer be called directly from package main).
func TestBuildConfigCoreOpts_Postgres_SchemaMismatch(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Apply all migrations, then simulate lag by removing entries for versions > 3
	// so VerifyExpectedVersion (inside percellpg.Resolve) sees actual < expected.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), migration.PlatformNamespace)
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations initially")

	_, execErr := pool.DB().Exec(ctx,
		"DELETE FROM schema_migrations_platform WHERE version_id > 3")
	require.NoError(t, execErr, "deleting version records must succeed")
	_ = pool.Close(ctx)

	// Drive the full env→provision path on the lagged DB; provisionPostgres opens
	// the pool (from the percellpg-agreed DSN) and runs verifyPGPreconditions,
	// which must fail-closed.
	setRealModeEnv(t, dsn)
	shared, locals, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	provErr := provisionCapabilities(ctx, shared, locals)
	for _, p := range locals.poolMRs {
		_ = p.Close(ctx)
	}

	require.Error(t, provErr, "provisionCapabilities must fail-closed when schema is lagged")
	assert.Contains(t, provErr.Error(), "schema guard",
		"error must mention schema guard")
}

// TestIntegration_AdminExists_OrphanSwept was deleted by PR #392 follow-up:
// the entire Sweep / Cleaner / orphan-credfile machinery was removed when
// initialadmin moved to the env-driven persistent startup credential model
// (ADR §D3 (delete bootstrap mode) + §D2 (operator credential)). There is no longer a credential file to sweep, so the
// test had no real semantics to assert.
