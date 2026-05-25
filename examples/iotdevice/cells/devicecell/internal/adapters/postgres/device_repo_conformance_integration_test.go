//go:build integration

package postgres

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain/conformance"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/tests/testutil"
)

// TestPGDeviceRepository_Conformance enrolls the PG implementation in the
// shared device-repo conformance suite (same suite drives mem behaviour).
//
// One PG testcontainer is shared across all sub-tests; each sub-test factory
// call TRUNCATEs `devices` for a pristine view without paying the
// container-start cost per sub-test. This matches the shared-container
// pattern used by adapters/postgres command_queue conformance.
//
// ref: cells/accesscore/internal/adapters/postgres/user_repo_conformance_integration_test.go
func TestPGDeviceRepository_Conformance(t *testing.T) {
	testutil.RequireDocker(t)
	pool, txMgr, terminate := setupSharedDeviceRepoPG(t)
	t.Cleanup(terminate)

	factory := func(t *testing.T) (domain.DeviceRepository, persistence.TxRunner, func() time.Time, func()) {
		t.Helper()
		resetDeviceRepoSchema(t, pool)
		repo, err := NewPGDeviceRepository(pool.DB(), txMgr, clock.Real())
		require.NoError(t, err)
		return repo, txMgr, time.Now, func() {}
	}

	conformance.RunDeviceRepoConformance(t, factory, conformance.Features{
		RequiresAmbientTx: true,
	})
}

// setupSharedDeviceRepoPG spins ONE testcontainer + migrates schema once for
// the whole conformance suite. terminate closes the pool and stops the
// container in t.Cleanup.
func setupSharedDeviceRepoPG(t *testing.T) (*adapterpg.Pool, *adapterpg.TxManager, func()) {
	t.Helper()
	// Required by archtest TestTestcontainerHelpersRequireDockerBeforeRun:
	// the same function that calls tcpostgres.Run must also call RequireDocker
	// (RequireDocker is idempotent — caller already invoking is harmless).
	testutil.RequireDocker(t)
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	terminate := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}
	return pool, adapterpg.NewTxManager(pool), terminate
}

// resetDeviceRepoSchema TRUNCATEs the devices table between sub-tests so each
// case starts from an empty state. RESTART IDENTITY CASCADE clears any
// dependent rows that might have been seeded (commands.device_id FK would
// cascade-reject otherwise — but devicecell's domain repo only owns devices).
func resetDeviceRepoSchema(t *testing.T, pool *adapterpg.Pool) {
	t.Helper()
	_, err := pool.DB().Exec(context.Background(), "TRUNCATE TABLE devices RESTART IDENTITY CASCADE")
	require.NoError(t, err, "truncate devices")
}

// TestPGDeviceRepository_RepoReadinessConformance wires PGDeviceRepository through
// the shared RepoHealthProber conformance harness:
//   - healthy: a fully migrated repo returns nil from RepoReady.
//   - broken: a repo whose devices table has been dropped returns non-nil.
//
// Each instance uses an isolated per-test DB (setupSharedDeviceRepoPG) so the
// DROP TABLE for the broken scenario does not affect the healthy pool's devices table.
func TestPGDeviceRepository_RepoReadinessConformance(t *testing.T) {
	testutil.RequireDocker(t)
	ctx := context.Background()

	// healthy: per-test DB with full migrations — devices table intact.
	healthyPool, healthyTxMgr, healthyTerminate := setupSharedDeviceRepoPG(t)
	t.Cleanup(healthyTerminate)
	healthy, err := NewPGDeviceRepository(healthyPool.DB(), healthyTxMgr, clock.Real())
	require.NoError(t, err)

	// broken: per-test DB with migrations applied, then devices table dropped
	// to simulate schema drift / missing migration.
	brokenPool, brokenTxMgr, brokenTerminate := setupSharedDeviceRepoPG(t)
	t.Cleanup(brokenTerminate)
	_, dropErr := brokenPool.DB().Exec(ctx, "DROP TABLE IF EXISTS devices CASCADE")
	require.NoError(t, dropErr, "drop devices table for broken scenario")
	broken, err := NewPGDeviceRepository(brokenPool.DB(), brokenTxMgr, clock.Real())
	require.NoError(t, err)

	celltest.RunRepoReadinessConformance(t, "devicecell-pg", healthy, broken)
}

// testAdapterMigrationsFS returns the shared adapters/postgres migration FS.
// Mirrors cells/accesscore/internal/adapters/postgres helpers — Go _test.go
// files cannot be imported across packages, so we duplicate the accessor.
func testAdapterMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}
