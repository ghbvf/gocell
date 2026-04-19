//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
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

	mode, opts, gotPool, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, kernelmetrics.NopProvider{})

	require.NoError(t, err, "buildConfigCoreOpts must succeed with a fully migrated DB")
	assert.Equal(t, "postgres", mode, "mode must be 'postgres'")
	assert.NotNil(t, opts, "opts must be non-nil")
	require.NotNil(t, gotPool, "pool must be non-nil on success")
	require.NotNil(t, relay, "relay worker must be non-nil on success (A11 wire guard)")

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

	mode, opts, gotPool, relay, err := buildConfigCoreOpts(ctx, discardPublisher{}, kernelmetrics.NopProvider{})

	require.Error(t, err, "buildConfigCoreOpts must return error when schema is lagged")
	assert.Contains(t, err.Error(), "schema guard",
		"error must mention schema guard")
	assert.Equal(t, "postgres", mode, "mode is still returned on error")
	assert.Nil(t, opts, "opts must be nil on schema mismatch")
	// pool must be nil — main.go closes and zeroes pool before returning error.
	assert.Nil(t, gotPool, "pool must be nil (was closed on schema mismatch)")
	assert.Nil(t, relay, "relay must be nil on schema mismatch (error path)")
}

// writeExpiredCredFile writes a minimal credential file with expires_at set to
// one hour in the past. Mimics the format produced by initialadmin.formatPayload
// without importing the internal package. The file is written with mode 0o600
// to satisfy RemoveCredentialFile's permission check.
func writeExpiredCredFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	expiresAt := time.Now().Add(-time.Hour).UTC()
	content := fmt.Sprintf(
		"# GoCell initial admin credential\n"+
			"username=admin\n"+
			"password=hunter2\n"+
			"expires_at=%d\n",
		expiresAt.Unix(),
	)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// TestIntegration_AdminExists_OrphanSwept verifies P1-16: SweepHook removes an
// expired credential file on startup, even when EnsureAdmin returns early because
// an admin already exists.
//
// Execution order in bootstrap.Run:
//
//	Step 3-4: asm.StartWithConfig → EnsureAdmin (admin exists → early return, no new cred file written)
//	Step 4.6: Lifecycle.Start → SweepHook.OnStart removes expired orphan cred file
//	Step 7:   TCP listen (may fail in sandbox — acceptable; sweep already ran)
func TestIntegration_AdminExists_OrphanSwept(t *testing.T) {
	stateDir := t.TempDir()
	credPath := filepath.Join(stateDir, "initial_admin_password")

	// Pre-condition: write an expired orphan credential file simulating a prior
	// run where the cleanup worker never fired (e.g. adminExists==true path).
	writeExpiredCredFile(t, credPath)

	// Confirm file exists before bootstrap.
	_, err := os.Stat(credPath)
	require.NoError(t, err, "orphan credential file must exist before bootstrap")

	// Configure env for run().
	t.Setenv("GOCELL_STATE_DIR", stateDir)
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-sweep-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	// Use a short-lived context: long enough for assembly init + lifecycle start
	// (Steps 3-4.6), but we accept context.Canceled, sandbox-bind, or
	// isBindError as acceptable outcomes — Sweep runs before TCP listen (Step 7).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runErr := run(ctx)

	// Only context.Canceled, deadline-exceeded, EPERM, and listen failures are
	// acceptable (sandbox may block TCP). Any other error signals a regression.
	if runErr != nil {
		acceptable := errors.Is(runErr, context.Canceled) ||
			errors.Is(runErr, context.DeadlineExceeded) ||
			errors.Is(runErr, syscall.EPERM) ||
			isBindError(runErr)
		if !acceptable {
			t.Fatalf("unexpected startup error (P1-16 regression): %v", runErr)
		}
	}

	// Assert: the expired credential file was removed by SweepHook (Step 4.6).
	// If the file still exists, SweepHook did not run — P1-16 gap is not closed.
	_, statErr := os.Stat(credPath)
	assert.True(t, errors.Is(statErr, os.ErrNotExist),
		"P1-16: expired credential file must be removed by SweepHook; got stat error: %v", statErr)
}
