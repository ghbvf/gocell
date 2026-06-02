package accesscore_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/accesscore"
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
// After Wave-1 #1423, accesscore.Provide no longer requires auditcore's
// BootstrapLedgerStore export — the cross-module handoff channel has been
// deleted. accesscore now publishes event.auth.bootstrap-failed.v1 via outbox
// and auditcore subscribes. The two modules are fully independent at Provide time.
func TestModule_Provide_MemMode(t *testing.T) {
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "admin-test-pass")

	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	res, err := accesscore.Module().Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, res.Cell)
	assert.Equal(t, "accesscore", res.Cell.ID())
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

	topo, err := bootstrap.NewTopology("", "memory", false)
	require.NoError(t, err)

	shared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
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
	})
	require.NoError(t, err)
	return shared
}
