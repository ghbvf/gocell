//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/crypto"
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

// TestBuildConfigCoreOpts_Postgres_SchemaMatched verifies that buildConfigCoreOpts
// returns (non-nil cell options, non-empty bootstrap opts, nil error) when a real
// PostgreSQL container is available and all migrations have been applied.
//
// Pool provisioning has moved to provisionCapabilities; this test opens a pool
// directly, wraps it into a capability.PGProvider, and injects it so buildConfigCoreOpts
// can consume it without opening its own pool.
func TestBuildConfigCoreOpts_Postgres_SchemaMatched(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-apply all migrations so schema version matches the binary.
	migPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(migPool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")
	_ = migPool.Close(ctx)

	// Open a fresh pool for the test; wrap into PGProvider for injection.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "open pool for test must succeed")
	defer func() { _ = pool.Close(ctx) }()

	txMgr := adapterpg.NewTxManager(pool)
	writer := adapterpg.NewOutboxWriter(clock.Real())
	pgProvider := capability.NewPGProvider(txMgr, writer, pool.DB())

	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")

	result, err := buildConfigCoreOpts(ConfigCoreModuleConfig{
		Topology:         bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		PG:               pgProvider,
		Publisher:        discardPublisher{},
		MetricsProvider:  kernelmetrics.NopProvider{},
		ValueTransformer: crypto.NoopTransformer{},
		Clock:            clock.Real(),
	})

	require.NoError(t, err, "buildConfigCoreOpts must succeed with a fully migrated DB")
	assert.NotNil(t, result.CellOptions, "cellOpts must be non-nil")
	// Relay is registered via bootstrap opts, not via PoolResource.
	assert.NotEmpty(t, result.BootstrapOpts, "bootstrapOpts must carry relay ManagedResource (A11 wire guard)")
}

// TestBuildConfigCoreOpts_Postgres_SchemaMismatch verifies that
// verifyPGPreconditions returns an error (with schema guard message) when the
// DB schema version does not match the binary.
//
// Schema verification has moved to provisionPostgres / verifyPGPreconditions;
// this test calls verifyPGPreconditions directly so the guard is still covered
// at integration level.
func TestBuildConfigCoreOpts_Postgres_SchemaMismatch(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	ctx := context.Background()

	// Apply only migrations up to version 3 by applying all then deleting newer
	// records from the tracking table — simulating a lagged DB.
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "pool for migration prep must succeed")

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations initially")

	// Simulate lag: remove entries for versions > 3 so VerifyExpectedVersion sees
	// actual < expected and returns a schema mismatch error.
	_, execErr := pool.DB().Exec(ctx,
		"DELETE FROM schema_migrations WHERE version_id > 3")
	require.NoError(t, execErr, "deleting version records must succeed")

	// Schema verification now lives in verifyPGPreconditions (provisionPostgres).
	// Call it directly to assert the schema guard fires on a lagged DB.
	err = verifyPGPreconditions(ctx, pool)

	_ = pool.Close(ctx)

	require.Error(t, err, "verifyPGPreconditions must return error when schema is lagged")
	assert.Contains(t, err.Error(), "schema guard",
		"error must mention schema guard")
}

// TestIntegration_AdminExists_OrphanSwept was deleted by PR #392 follow-up:
// the entire Sweep / Cleaner / orphan-credfile machinery was removed when
// initialadmin moved to the env-driven persistent startup credential model
// (ADR §D3 (delete bootstrap mode) + §D2 (operator credential)). There is no longer a credential file to sweep, so the
// test had no real semantics to assert.
