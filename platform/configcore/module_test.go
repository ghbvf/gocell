package configcore_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/platform/configcore"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// testJWTAccessTokenTTL is the JWT TTL used in mem-mode tests.
const testJWTAccessTokenTTL = 15 * time.Minute

func TestModule_ReturnsNonNilCellModule(t *testing.T) {
	m := configcore.Module()
	require.NotNil(t, m, "Module() must return a non-nil composition.CellModule")
}

func TestModule_CorrectID(t *testing.T) {
	m := configcore.Module()
	assert.Equal(t, "configcore", m.ID(), "Module ID must be 'configcore'")
}

func TestModule_ImplementsCellModule(*testing.T) {
	_ = []composition.CellModule{configcore.Module()}
}

func TestModule_WithKeyProviderOverride_DoesNotPanic(t *testing.T) {
	m := configcore.Module(configcore.WithKeyProviderOverride(nil))
	require.NotNil(t, m)
}

// TestModule_Provide_MemMode exercises configcore.Module().Provide with a
// memory-mode SharedDeps (no Postgres).  ConfigKeyProvider is left nil
// (no-key/passthrough provider); ConfigStaleCipherInc is a no-op.
func TestModule_Provide_MemMode(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	c, _, _, err := configcore.Module().Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "configcore", c.ID())
}

// buildMemSharedDeps constructs a memory-mode *composition.SharedDeps for tests.
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
		// ConfigKeyProvider: nil — uses NoopTransformer (no encryption).
		ConfigStaleCipherInc: func() {},
	}
	require.NoError(t, shared.Validate())
	return shared
}
