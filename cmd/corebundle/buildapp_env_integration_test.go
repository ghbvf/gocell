//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
)

// applyMigrationsForMain applies all schema migrations to the given DSN using
// the canonical MigrationsFS embedded in the adapters/postgres package. This
// mirrors the pattern in TestBuildConfigCoreOpts_Postgres_SchemaMatched.
func applyMigrationsForMain(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "migration prep pool must open")
	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")
	_ = pool.Close(ctx)
}

// setRealModeEnv sets all environment variables required for real/postgres
// adapter mode. dsn is written to GOCELL_CONFIGCORE_DATABASE_URL.
// Use with t.Setenv so values are cleaned up automatically.
func setRealModeEnv(t *testing.T, dsn string) {
	t.Helper()

	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")

	// Cross-cutting required env
	t.Setenv("GOCELL_JWT_ISSUER", "smoke-buildapp-env")
	t.Setenv("GOCELL_JWT_AUDIENCE", "smoke")
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_HTTP_HEALTH_ADDR", ":9091")
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	// F1: in real mode, in-memory nonce store requires explicit single-pod opt-in.
	t.Setenv("GOCELL_SINGLE_POD", "1")

	// Production control-plane tokens
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_METRICS_TOKEN", "test-metrics-token")
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "test-verbose-token")

	// auditcore cell
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")

	// configcore cell
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")

	// accesscore cell
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cursor-key-32b-padded-x!!")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testpassword123")
}

// TestBuildApp_Postgres_UsesConfigCoreDatabaseURL verifies the complete
// env-to-pool contract: setting GOCELL_CONFIGCORE_DATABASE_URL=<dsn> and
// running the full LoadSharedDepsFromEnv → provisionCapabilities → BuildApp path
// results in a successfully wired assembly backed by a live PostgreSQL pool.
//
// This test covers F5: the env→pool path had zero automated coverage because
// all existing integration tests bypassed LoadPGConfig + LoadSharedDepsFromEnv
// by calling buildConfigCoreOpts directly. Post capability-provider refactor the
// pool is opened by provisionCapabilities (not by ConfigCoreModule.Provide), so
// the test must run that step before BuildApp.
func TestBuildApp_Postgres_UsesConfigCoreDatabaseURL(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D60s)
	defer cancel()

	// Apply migrations so the schema version matches the binary (required by
	// verifyPGPreconditions inside provisionCapabilities).
	applyMigrationsForMain(t, ctx, dsn)

	// LoadSharedDepsFromEnv → provisionCapabilities is the code path under test
	// (env vars → topology → LoadPGConfig → assembly pool → shared.PG).
	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed with all required env set")

	require.NoError(t, provisionCapabilities(ctx, shared),
		"provisionCapabilities must open the assembly pool from GOCELL_CONFIGCORE_DATABASE_URL")
	require.NotNil(t, shared.PG, "shared.PG must be provisioned from the DSN in postgres mode")
	require.NotNil(t, shared.poolMR, "shared.poolMR must hold the pool ManagedResource")
	defer func() { _ = shared.poolMR.Close(ctx) }()

	cells, _, err := BuildApp(ctx, shared,
		ConfigCoreModule{},
		// auditcore before accesscore — wires SharedDeps.BootstrapLedgerStore
		// for audit.NewBootstrapAuthFailObserver
		// (MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01).
		AuditCoreModule{},
		AccessCoreModule{},
	)
	require.NoError(t, err, "BuildApp must succeed: cell modules consume the injected shared.PG")
	require.Len(t, cells, 3, "BuildApp must return exactly 3 cells")
}

// TestProvisionCapabilities_Postgres_UsesConfigCoreDatabaseURL verifies that
// provisionCapabilities, when called after LoadSharedDepsFromEnv in postgres
// mode, opens the assembly pool from GOCELL_CONFIGCORE_DATABASE_URL and records
// it as shared.poolMR with a passing postgres_ready checker.
//
// This slim companion test isolates the pool-provisioning path (formerly
// ConfigCoreModule.Provide opened the pool; the capability-provider refactor
// moved it to provisionCapabilities) so the pool ManagedResource + its health
// check are verified independently of the full BuildApp assembly.
func TestProvisionCapabilities_Postgres_UsesConfigCoreDatabaseURL(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D60s)
	defer cancel()

	// Apply migrations so verifyPGPreconditions inside provisionCapabilities passes.
	applyMigrationsForMain(t, ctx, dsn)

	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	// provisionCapabilities opens the pool and records it as shared.poolMR.
	require.NoError(t, provisionCapabilities(ctx, shared),
		"provisionCapabilities must succeed with GOCELL_CONFIGCORE_DATABASE_URL set")
	require.NotNil(t, shared.PG, "shared.PG must be provisioned in postgres mode")
	require.NotNil(t, shared.poolMR, "provisionCapabilities must record the pool as shared.poolMR")

	pgRes := shared.poolMR

	// Verify the ManagedResource exposes a "postgres_ready" checker (the name used by
	// adapterpg.Pool, which directly implements ManagedResource) and that it
	// reports healthy against the live container started by setupPostgresForMain.
	checkers := pgRes.Checkers()
	pgChecker, ok := checkers["postgres_ready"]
	require.True(t, ok, "pool ManagedResource must expose a \"postgres_ready\" checker (adapterpg.Pool)")
	require.NoError(t, pgChecker(ctx), "postgres_ready checker must pass for the live container DSN")

	// Close the resource to avoid leaking the connection pool.
	// Ignore the error — the test has already passed at this point and pool
	// cleanup errors (e.g. context cancellation) should not fail the assertion.
	_ = pgRes.Close(ctx)
}
