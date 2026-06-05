//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
)

// applyMigrationsForMain applies all schema migrations to the given DSN using
// the canonical MigrationsFS embedded in the adapters/postgres package. This
// mirrors the pattern in TestBuildConfigCoreOpts_Postgres_SchemaMatched.
func applyMigrationsForMain(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "migration prep pool must open")
	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), migration.PlatformNamespace)
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

	// auditcore cell (two chains since issue #1121: relay + bootstrap)
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	t.Setenv("GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY", "prod-bootstrap-hmac-key-32bytes!")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")

	// configcore cell
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "local-aes")
	t.Setenv("GOCELL_CONFIGCORE_MASTER_KEY", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")

	// accesscore cell
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cursor-key-32b-padded-x!!")
	t.Setenv("GOCELL_ACCESSCORE_IP_HASH_SALT", "access-ip-hash-salt-32b-pad-test!!")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "testadmin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "testpassword123")
}

// TestCorebundlePG_UsesConfigCoreDatabaseURL verifies the complete
// env-to-pool contract: setting GOCELL_CONFIGCORE_DATABASE_URL=<dsn> and
// running the full LoadSharedDepsFromEnv → provisionCapabilities →
// composition.Builder.Build path results in a successfully wired assembly
// backed by a live PostgreSQL pool.
//
// This test covers F5: the env→pool path had zero automated coverage because
// all existing integration tests bypassed LoadPGConfig + LoadSharedDepsFromEnv
// by calling buildConfigCoreOpts directly. Post capability-provider refactor the
// pool is opened by provisionCapabilities (not by any cell module), so
// the test must run that step before Build.
func TestCorebundlePG_UsesConfigCoreDatabaseURL(t *testing.T) {
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
	shared, locals, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed with all required env set")

	require.NoError(t, provisionCapabilities(ctx, shared, locals),
		"provisionCapabilities must open the assembly pool from GOCELL_CONFIGCORE_DATABASE_URL")
	require.NotNil(t, shared.PG, "shared.PG must be provisioned from the DSN in postgres mode")
	require.NotNil(t, locals.poolMR, "locals.poolMR must hold the pool ManagedResource")
	defer func() { _ = locals.poolMR.Close(ctx) }()

	// Build via composition.New().With(mods...).Build() — the new public API.
	// A no-op RuntimeOptionsFunc is sufficient: we only need to verify that
	// the three platform modules (configcore/auditcore/accesscore) successfully
	// Provide their cells using shared.PG. The runtime bootstrap options (listener
	// auth, consumer base, etc.) are not under test here.
	_ = locals // locals used only for shared deps construction, not module wiring
	mods := generatedCellModules()
	app, err := composition.New(corebundleCellIDs()...).With(mods...).Build(ctx, shared,
		func(_ []cell.Cell) ([]bootstrap.Option, error) {
			return nil, nil
		})
	_ = app // app.Run not needed — Build success is the invariant
	require.NoError(t, err, "composition.Builder.Build must succeed: cell modules consume the injected shared.PG")
}

// TestProvisionCapabilities_Postgres_UsesConfigCoreDatabaseURL verifies that
// provisionCapabilities, when called after LoadSharedDepsFromEnv in postgres
// mode, opens the assembly pool from GOCELL_CONFIGCORE_DATABASE_URL and records
// it as locals.poolMR with a passing postgres_ready checker.
//
// This slim companion test isolates the pool-provisioning path (formerly
// ConfigCoreModule.Provide opened the pool; the capability-provider refactor
// moved it to provisionCapabilities) so the pool ManagedResource + its health
// check are verified independently of the full assembly Build.
func TestProvisionCapabilities_Postgres_UsesConfigCoreDatabaseURL(t *testing.T) {
	dsn, cleanup := setupPostgresForMain(t)
	defer cleanup()

	setRealModeEnv(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D60s)
	defer cancel()

	// Apply migrations so verifyPGPreconditions inside provisionCapabilities passes.
	applyMigrationsForMain(t, ctx, dsn)

	shared, locals, err := LoadSharedDepsFromEnv(ctx)
	require.NoError(t, err, "LoadSharedDepsFromEnv must succeed")

	// provisionCapabilities opens the pool and records it as locals.poolMR.
	require.NoError(t, provisionCapabilities(ctx, shared, locals),
		"provisionCapabilities must succeed with GOCELL_CONFIGCORE_DATABASE_URL set")
	require.NotNil(t, shared.PG, "shared.PG must be provisioned in postgres mode")
	require.NotNil(t, locals.poolMR, "provisionCapabilities must record the pool as locals.poolMR")

	pgRes := locals.poolMR

	// Verify the ManagedResource exposes a "postgres_ready" probe (the name used by
	// adapterpg.Pool, which directly implements ManagedResource) and that it
	// reports healthy against the live container started by setupPostgresForMain.
	var pgProbe interface{ Check(context.Context) error }
	for _, p := range pgRes.Probes() {
		if p.Name() == "postgres_ready" {
			pgProbe = p
			break
		}
	}
	require.NotNil(t, pgProbe, "pool ManagedResource must expose a \"postgres_ready\" probe (adapterpg.Pool)")
	require.NoError(t, pgProbe.Check(ctx), "postgres_ready probe must pass for the live container DSN")

	// Close the resource to avoid leaking the connection pool.
	// Ignore the error — the test has already passed at this point and pool
	// cleanup errors (e.g. context cancellation) should not fail the assertion.
	_ = pgRes.Close(ctx)
}
