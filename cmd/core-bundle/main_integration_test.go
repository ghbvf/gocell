//go:build integration

package main

import (
	"context"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
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

// TestBuildConfigCoreOpts_Postgres_SchemaMatched verifies that buildConfigCoreOpts
// returns (mode=="postgres", non-nil opts, non-nil pool, nil error) when a real
// PostgreSQL container is available and all migrations have been applied.
func TestBuildConfigCoreOpts_Postgres_SchemaMatched(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-apply all migrations so schema version matches the binary.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")
	pool.Close()

	// Set env so buildConfigCoreOpts picks the postgres path.
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_PG_DSN", dsn)

	mode, opts, gotPool, err := buildConfigCoreOpts(ctx)

	require.NoError(t, err, "buildConfigCoreOpts must succeed with a fully migrated DB")
	assert.Equal(t, "postgres", mode, "mode must be 'postgres'")
	assert.NotNil(t, opts, "opts must be non-nil")
	require.NotNil(t, gotPool, "pool must be non-nil on success")

	// Cleanup returned pool.
	gotPool.Close()
}

// TestBuildConfigCoreOpts_Postgres_SchemaMismatch verifies that buildConfigCoreOpts
// returns an error (with schema guard message) and a nil pool (pool.Close() was
// called inside) when the DB schema version does not match the binary.
func TestBuildConfigCoreOpts_Postgres_SchemaMismatch(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Apply only migrations up to version 3 by applying all then deleting newer
	// records from the tracking table — simulating a lagged DB.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations initially")

	// Simulate lag: remove entries for versions > 3 so VerifyExpectedVersion sees
	// actual < expected and returns a schema mismatch error.
	_, execErr := pool.DB().Exec(ctx,
		"DELETE FROM schema_migrations WHERE version_id > 3")
	require.NoError(t, execErr, "deleting version records must succeed")
	pool.Close()

	// Set env so buildConfigCoreOpts picks the postgres path.
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_PG_DSN", dsn)

	mode, opts, gotPool, err := buildConfigCoreOpts(ctx)

	require.Error(t, err, "buildConfigCoreOpts must return error when schema is lagged")
	assert.Contains(t, err.Error(), "schema guard",
		"error must mention schema guard")
	assert.Equal(t, "postgres", mode, "mode is still returned on error")
	assert.Nil(t, opts, "opts must be nil on schema mismatch")
	// pool must be nil — main.go closes and zeroes pool before returning error.
	assert.Nil(t, gotPool, "pool must be nil (was closed on schema mismatch)")
}
