//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// returns (non-nil ManagedResource, non-nil opts, nil error) when a real PostgreSQL
// container is available and all migrations have been applied.
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

	res, opts, err := buildConfigCoreOpts(ctx, discardPublisher{}, kernelmetrics.NopProvider{})

	require.NoError(t, err, "buildConfigCoreOpts must succeed with a fully migrated DB")
	require.NotNil(t, res, "ManagedResource must be non-nil on success")
	assert.NotNil(t, opts, "opts must be non-nil")
	require.NotNil(t, res.Worker(), "relay worker must be non-nil on success (A11 wire guard)")

	// Close pool via ManagedResource so pool.Close() is called correctly.
	require.NoError(t, res.Close())
}

// TestBuildConfigCoreOpts_Postgres_SchemaMismatch verifies that buildConfigCoreOpts
// returns an error (with schema guard message) and a nil ManagedResource when the
// DB schema version does not match the binary.
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

	res, opts, err := buildConfigCoreOpts(ctx, discardPublisher{}, kernelmetrics.NopProvider{})

	require.Error(t, err, "buildConfigCoreOpts must return error when schema is lagged")
	assert.Contains(t, err.Error(), "schema guard",
		"error must mention schema guard")
	assert.Nil(t, opts, "opts must be nil on schema mismatch")
	// ManagedResource must be nil — pool was closed inside buildConfigCoreOpts before returning error.
	assert.Nil(t, res, "ManagedResource must be nil on schema mismatch (error path, pool was closed)")
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

// TestIntegration_AdminExists_OrphanSwept verifies P1-16: Sweep removes an
// expired credential file during Cell.Init, before EnsureAdmin attempts to write
// a new one.
//
// NOTE on test scope: This test does not pre-populate the user repository with
// an admin record. run() uses an in-memory DB that starts empty, so EnsureAdmin
// will create a new admin user rather than taking the adminExists==true branch.
// The test therefore validates that Sweep executes inside runInitialAdminBootstrap
// (before EnsureAdmin) and removes the expired file — independently of EnsureAdmin's
// decision path. The adminExists==true causal guarantee is covered at unit level
// by sweep_test.go::TestSweep_AdminExistsDoesNotSkip (pure Sweep function).
//
// Note: The companion TestIntegration_AdminExists_FreshOrphan was removed:
// in-memory repositories reset per BuildApp call, making adminExists==true
// impossible to stage at the integration level. The fresh-orphan causal
// chain (Sweep returns Cleaner → sink) is strictly covered by the unit
// test in cell_initialadmin_test.go::TestInit_BootstrapAdminExists_
// FreshOrphanFile_SweepCleanerRegistered.
//
// Execution order in bootstrap.Run:
//
//	Step 3-4: asm.StartWithConfig → Cell.Init → runInitialAdminBootstrap:
//	          1. Sweep removes expired orphan cred file
//	          2. EnsureAdmin creates admin (in-memory DB: empty → creates user + writes new cred file)
//	Step 7:   TCP listen (may fail in sandbox — acceptable; sweep + EnsureAdmin already ran)
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

	// Assert: the expired credential file was swept and a fresh one written by
	// EnsureAdmin. The key P1-16 invariant is that startup succeeded without
	// "credential file already exists" — proven by the no-unexpected-error check
	// above. Additionally verify the file content is fresh (expires_at in future),
	// confirming Sweep removed the expired orphan before EnsureAdmin wrote a new one.
	rawContent, readErr := os.ReadFile(credPath)
	require.NoError(t, readErr, "P1-16: credential file must exist after bootstrap (written by EnsureAdmin)")
	assert.Contains(t, string(rawContent), "expires_at=",
		"P1-16: fresh credential file must contain expires_at field")
	// Verify expires_at is in the future (file is newly written, not the expired orphan).
	for _, line := range strings.Split(string(rawContent), "\n") {
		if strings.HasPrefix(line, "expires_at=") {
			unixStr := strings.TrimPrefix(line, "expires_at=")
			var unixSec int64
			if _, scanErr := fmt.Sscanf(unixStr, "%d", &unixSec); scanErr == nil {
				assert.Greater(t, unixSec, time.Now().Unix(),
					"P1-16: fresh credential file expires_at must be in the future; Sweep must have removed the expired orphan")
			}
			break
		}
	}
}
