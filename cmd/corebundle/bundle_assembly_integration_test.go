//go:build integration

package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// buildBootstrapFromShared is the test-path assembly helper, equivalent to the
// production run() flow. It owns the PrimaryListener registration so the JWT
// policy (PolicyJWTFromAssembly) is wired with the assembly that BuildApp
// constructs internally. Tests supply the primary net.Listener and any extra
// options (typically WithListener for InternalListener/HealthListener,
// WithManagedResource, etc.). Uses memory topology and AccessCoreModule with
// a fast-bcrypt option.
func buildBootstrapFromShared(
	t *testing.T, shared *SharedDeps, primaryLn net.Listener, extra ...bootstrap.Option,
) (*bootstrap.Bootstrap, error) {
	t.Helper()
	return buildBootstrapFromSharedWithModules(t, shared, primaryLn, []CellModule{
		ConfigCoreModule{},
		AccessCoreModule{},
		AuditCoreModule{},
	}, nil, extra...)
}

// TestBuildBootstrap_AssemblyHasAllCells verifies that BuildApp registers
// configcore, accesscore, and auditcore in durable (postgres) mode. We check
// via health + /readyz which would fail if any cell fails to init.
//
// Requires a live PostgreSQL instance (started via testcontainers);
// gated on the integration build tag.
func TestBuildBootstrap_AssemblyHasAllCells(t *testing.T) {
	ctx := context.Background()

	// Provision a real PG instance, run all migrations, and expose the DSN to
	// each CellModule via per-cell env vars.
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "create migration pool")
	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))
	require.NoError(t, pool.Close(ctx))

	// Expose the single shared DSN to every CellModule (all three cells share one
	// DB in this test — mirrors the per-cell default in non-split deployments).
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_ACCESSCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_AUDITCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")

	shared := newValidatedSharedDeps(t, bootstrap.Topology{
		StorageBackend: "postgres",
		AdapterMode:    "real",
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	healthOpt := bootstrap.WithListener(
		cell.HealthListener, healthLn.Addr().String(),
		[]cell.ListenerAuth{cell.AuthNone{}}, bootstrap.WithListenerNet(healthLn))
	app, err := buildBootstrapFromShared(t, shared, ln,
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		healthOpt)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(runCtx) }()

	healthAddr := healthLn.Addr().String()
	waitForHealthy(t, healthAddr)

	// /readyz confirms all three cells started and registered their probes.
	resp, err := http.Get("http://" + healthAddr + "/readyz")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Logf("close resp body: %v", err)
		}
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"all three cells (configcore, accesscore, auditcore) must be healthy")

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectAsyncSettle):
		t.Fatal("full assembly bootstrap did not shut down in time")
	}
}
