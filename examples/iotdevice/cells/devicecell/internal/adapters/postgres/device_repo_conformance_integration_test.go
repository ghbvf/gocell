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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/tests/testutil"
)

// TestPGDeviceRepository_Conformance enrolls the PG implementation in the
// shared device-repo conformance suite (same suite drives mem behaviour).
//
// ref: cells/accesscore/internal/adapters/postgres/user_repo_conformance_integration_test.go
func TestPGDeviceRepository_Conformance(t *testing.T) {
	conformance.RunDeviceRepoConformance(t, pgDeviceRepoFactory(t), conformance.Features{
		RequiresAmbientTx: true,
	})
}

func pgDeviceRepoFactory(t *testing.T) conformance.DeviceRepoFactory {
	t.Helper()
	return func(t *testing.T) (domain.DeviceRepository, persistence.TxRunner, func() time.Time, func()) {
		t.Helper()
		repo, txRunner, cleanup := setupPGDeviceRepo(t)
		return repo, txRunner, time.Now, cleanup
	}
}

// setupPGDeviceRepo spins a fresh PG testcontainer, runs all migrations, and
// returns a PGDeviceRepository wired to a NewTxManager + clock.Real().
//
// Each call creates an isolated container so conformance sub-tests cannot
// observe each other's writes. testutil.RequireDocker skips the test cleanly
// when Docker is unavailable.
func setupPGDeviceRepo(t *testing.T) (*PGDeviceRepository, persistence.TxRunner, func()) {
	t.Helper()
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

	txMgr := adapterpg.NewTxManager(pool)
	repo, err := NewPGDeviceRepository(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return repo, txMgr, cleanup
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
