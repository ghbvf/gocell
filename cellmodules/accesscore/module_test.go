package accesscore_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/accesscore"
	platformauditcore "github.com/ghbvf/gocell/cellmodules/auditcore"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// testJWTAccessTokenTTL is used for the JWT issuer in tests.
const testJWTAccessTokenTTL = 15 * time.Minute

func TestModule_ReturnsNonNilCellModule(t *testing.T) {
	m := accesscore.Module()
	require.NotNil(t, m, "Module() must return a non-nil composition.CellModule")
}

func TestModule_CorrectID(t *testing.T) {
	m := accesscore.Module()
	assert.Equal(t, "accesscore", m.ID(), "Module ID must be 'accesscore'")
}

func TestModule_ImplementsCellModule(*testing.T) {
	_ = []composition.CellModule{accesscore.Module()}
}

// TestModule_Provide_MemMode exercises accesscore.Module().Provide with a
// memory-mode SharedDeps (no Postgres, no Redis).
//
// accesscore.Provide requires shared.BootstrapLedgerStore (wired by auditcore),
// so we first run auditcore.Module().Provide to populate it — mirroring the real
// composition assembly order (auditcore before accesscore).
func TestModule_Provide_MemMode(t *testing.T) {
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "admin-test-pass")

	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	// auditcore must run first: it populates shared.BootstrapLedgerStore.
	_, _, _, err := platformauditcore.Module().Provide(ctx, shared)
	require.NoError(t, err, "auditcore.Provide must succeed before accesscore")
	require.NotNil(t, shared.BootstrapLedgerStore, "auditcore.Provide must set BootstrapLedgerStore")

	c, _, _, err := accesscore.Module().Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "accesscore", c.ID())
}

// buildMemSharedDeps constructs a memory-mode *composition.SharedDeps suitable
// for all three platform module Provide tests.
func buildMemSharedDeps(t *testing.T) *composition.SharedDeps {
	t.Helper()
	clk := clock.Real()
	eb := eventbus.New(clk)
	claimer := idempotency.NewInMemClaimer(clk)

	privKey, pubKey, err := auth.GenerateRSAKeyPair()
	require.NoError(t, err)
	ks, err := auth.NewKeySet(privKey, pubKey, clk)
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(ks, "test-issuer", testJWTAccessTokenTTL, clk,
		auth.WithIssuerAudiencesFromSlice([]string{"test-aud"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clk,
		auth.WithExpectedAudiences("test-aud"),
		auth.WithExpectedIssuer("test-issuer"))
	require.NoError(t, err)

	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-for-internal-ring!"), nil)
	require.NoError(t, err)

	shared := &composition.SharedDeps{
		Clock:                  clk,
		Topology:               bootstrap.Topology{AdapterMode: "dev", StorageBackend: "memory"},
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        kernelmetrics.NopProvider{},
		EventBus:               eb,
		ConfigEventCollector:   obmetrics.NoopConfigEventCollector{},
		EventbusCacheCollector: obmetrics.NoopEventbusCacheCollector{},
		ConsumerClaimer:        claimer,
		InternalHMACRing:       ring,
		PrimaryHTTPAddr:        ":8080",
		InternalHTTPAddr:       "127.0.0.1:9090",
		HealthHTTPAddr:         "127.0.0.1:9091",
		VerboseDisabled:        true,
		ConfigStaleCipherInc:   func() {},
	}
	require.NoError(t, shared.Validate())
	return shared
}
