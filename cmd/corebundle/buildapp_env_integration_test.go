//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/auth"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// applyMigrationsForMain applies all schema migrations to the given DSN using
// the canonical MigrationsFS embedded in the adapters/postgres package. This
// mirrors the pattern in TestBuildConfigCoreOpts_Postgres_SchemaMatched.
func applyMigrationsForMain(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "migration prep pool must open")
	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
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
}

// TestBuildApp_Postgres_UsesConfigCoreDatabaseURL verifies the complete
// env-to-pool contract: setting GOCELL_CONFIGCORE_DATABASE_URL=<dsn> and
// running the full LoadSharedDepsFromEnv → BuildApp path results in a
// successfully wired assembly with a live PostgreSQL connection.
//
// This test covers F5: the env→pool path had zero automated coverage because
// all existing integration tests bypassed LoadPGConfig + LoadSharedDepsFromEnv
// by calling buildConfigCoreOpts directly.
func TestBuildApp_Postgres_UsesConfigCoreDatabaseURL(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Apply migrations so the schema version matches the binary (required by
	// the schema guard in buildConfigCoreOpts).
	applyMigrationsForMain(t, ctx, dsn)

	// Walk the full LoadSharedDepsFromEnv → BuildApp path — this is the
	// code path under test (env vars → topology → LoadPGConfig → pool).
	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed with all required env set")

	cells, cellOpts, err := BuildApp(ctx, shared,
		ConfigCoreModule{},
		AccessCoreModule{ForceBootstrap: true, InitialAdminOpts: fastAdminBootstrapOpts()},
		AuditCoreModule{},
	)
	require.NoError(t, err, "BuildApp must succeed: env→pool wiring must complete without error")
	require.Len(t, cells, 3, "BuildApp must return exactly 3 cells")

	// cellOpts is non-nil and contains at least one WithManagedResource option
	// (the PGResource for configcore). We do not type-assert into the opaque
	// functional option; the non-empty opts slice is the observable proxy.
	assert.NotEmpty(t, cellOpts, "cellOpts must include at least one PG managed-resource option")
}

// TestConfigCoreModule_Provide_UsesConfigCoreDatabaseURL verifies that
// ConfigCoreModule.Provide, when called after LoadSharedDepsFromEnv, returns
// a non-nil provisional ManagedResource whose postgres_ready checker passes.
//
// This slim companion test isolates the Provide path so that the PGResource
// type and its health check are verified independently of the full BuildApp
// assembly.
func TestConfigCoreModule_Provide_UsesConfigCoreDatabaseURL(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Apply migrations so the schema guard in Provide passes.
	applyMigrationsForMain(t, ctx, dsn)

	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	// Call Provide directly so we can inspect the provisional ManagedResource.
	_, _, provisional, err := ConfigCoreModule{}.Provide(ctx, shared)
	require.NoError(t, err, "ConfigCoreModule.Provide must succeed with GOCELL_CONFIGCORE_DATABASE_URL set")
	require.NotEmpty(t, provisional, "ConfigCoreModule.Provide must return at least one ManagedResource for postgres topology")

	pgRes := provisional[0]

	// Verify the ManagedResource exposes a "postgres_ready" checker (the name used by
	// adapterpg.PGResource) and that it reports healthy against the live container
	// started by setupPostgresForMain.
	checkers := pgRes.Checkers()
	pgChecker, ok := checkers["postgres_ready"]
	require.True(t, ok, "ManagedResource must expose a \"postgres_ready\" checker (adapterpg.PGResource default name)")
	require.NoError(t, pgChecker(ctx), "postgres_ready checker must pass for the live container DSN")

	// Close the resource to avoid leaking the connection pool.
	// Ignore the error — the test has already passed at this point and pool
	// cleanup errors (e.g. context cancellation) should not fail the assertion.
	_ = pgRes.Close(ctx)
	_ = provisional // satisfy linter: ensure we use the variable after nil-check

	// Confirm the interface is satisfied at runtime (compile-time is implicit via
	// the provisional[0] read above). The conversion documents the expected type
	// without tripping staticcheck QF1011.
	_ = kernellifecycle.ManagedResource(pgRes)
}

type conflictingRenewalMetricsProvider struct {
	fakeKeyProvider
}

func (conflictingRenewalMetricsProvider) RenewalMetrics() []prom.Collector {
	return []prom.Collector{
		prom.NewCounter(prom.CounterOpts{
			Namespace: configStaleCipherOpts.Namespace,
			Subsystem: configStaleCipherOpts.Subsystem,
			Name:      configStaleCipherOpts.Name,
			Help:      "conflicting help text forces prometheus registration failure",
		}),
	}
}

func TestConfigCoreModule_Provide_RollsBackPGResourceOnRenewalMetricError(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	applyMigrationsForMain(t, ctx, dsn)

	shared, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	_, _, provisional, err := ConfigCoreModule{
		KeyProviderOverride: &conflictingRenewalMetricsProvider{},
	}.Provide(ctx, shared)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "register key provider metrics")
	assert.Empty(t, provisional, "failed Provide must not return provisional resources")
	assert.Nil(t, shared.SharedPGPool, "failed Provide must clear the shared PG pool after rollback")
}
