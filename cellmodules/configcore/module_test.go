package configcore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/configcore"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/worker"
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

// TestModule_Provide_MemMode_SelfBuild_NilOverrideTriggersEnvPath verifies that
// WithKeyProviderOverride(nil) falls through to the self-build env path.
// In memory mode with no GOCELL_CONFIGCORE_KEY_PROVIDER set (no env), the
// self-build returns (nil, nil) → NoopTransformer → Provide succeeds.
func TestModule_Provide_MemMode_SelfBuild_NilOverrideTriggersEnvPath(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)
	// Explicitly unset env so self-build returns no-key sentinel.
	t.Setenv("GOCELL_CONFIGCORE_KEY_PROVIDER", "")

	// nil override → self-build path; memory mode + no provider → Noop.
	res, err := configcore.Module(configcore.WithKeyProviderOverride(nil)).Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, res.Cell)
}

// TestModule_Provide_MemMode exercises configcore.Module().Provide with a
// memory-mode SharedDeps (no Postgres).  ConfigKeyProvider is left nil
// (no-key/passthrough provider); ConfigStaleCipherInc is a no-op.
func TestModule_Provide_MemMode(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	res, err := configcore.Module().Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, res.Cell)
	assert.Equal(t, "configcore", res.Cell.ID())
}

// errCounterVecProvider is a metrics Provider whose CounterVec always fails,
// used to exercise the configcore self-built collector error branches (#1413):
// stale-cipher / eventbus-cache construction failures must propagate out of
// Provide rather than being swallowed.
type errCounterVecProvider struct{ kernelmetrics.NopProvider }

func (errCounterVecProvider) CounterVec(kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return nil, errors.New("counter registration failed")
}

// TestModule_Provide_CollectorRegistrationError verifies that when the kernel
// MetricsProvider rejects collector registration, configcore.Provide fail-fasts
// with a wrapped error (the self-built stale-cipher collector is constructed
// before the cell, so its failure surfaces first).
func TestModule_Provide_CollectorRegistrationError(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)
	// Swap in a Provider whose CounterVec fails; Provide builds the stale-cipher
	// and eventbus-cache collectors from shared.MetricsProvider (#1413).
	shared.MetricsProvider = errCounterVecProvider{}

	_, err := configcore.Module().Provide(ctx, shared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector")
}

// fakeKeyProvider is a minimal kcrypto.KeyProvider for testing the
// WithKeyProviderOverride non-nil path. It satisfies the interface without
// performing any real cryptographic operations.
type fakeKeyProvider struct{}

func (fakeKeyProvider) Current(_ context.Context) (kcrypto.KeyHandle, error) {
	return nil, errors.New("fakeKeyProvider: not implemented")
}

func (fakeKeyProvider) ByID(_ context.Context, _ string) (kcrypto.KeyHandle, error) {
	return nil, errors.New("fakeKeyProvider: not implemented")
}

func (fakeKeyProvider) Rotate(_ context.Context) (string, error) {
	return "", errors.New("fakeKeyProvider: not implemented")
}

// TestModule_Provide_MemMode_WithKeyProviderOverride_NonNil verifies that passing
// a non-nil KeyProvider via WithKeyProviderOverride bypasses the self-build path
// and propagates the override to the cell, and that Provide succeeds in memory mode.
// This covers the override!=nil branch of resolveKeyProvider end-to-end.
//
// It also pins the ok==false half of buildConfigCoreResult's
// kp.(kernellifecycle.ManagedResource) type assertion: a KeyProvider that is NOT
// a ManagedResource (the local-aes shape) must NOT be surfaced as a resource, so
// res.Resources stays empty. The ok==true (vault-transit shape) complement is
// TestModule_Provide_KeyProviderImplementingManagedResource_SurfacedInResources.
func TestModule_Provide_MemMode_WithKeyProviderOverride_NonNil(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	res, err := configcore.Module(configcore.WithKeyProviderOverride(fakeKeyProvider{})).Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, res.Cell)
	assert.Empty(t, res.Resources,
		"a KeyProvider that does not implement ManagedResource (local-aes shape) must not be surfaced as a resource")
}

// fakeManagedKeyProvider is a fakeKeyProvider that ALSO implements
// kernel/lifecycle.ManagedResource — the same dual shape the real vault-transit
// *adaptervault.TransitKeyProvider presents to buildConfigCoreResult. Its only
// job is to BE a ManagedResource so the test can assert that a resource-bearing
// KeyProvider is surfaced in ModuleResult.Resources (where Builder.Build derives
// the steady-state WithManagedResource registration + pre-Run rollback).
//
// LAYER BOUNDARY (F6): the configcore layer's responsibility for the
// vault-transit success path is exactly this routing — a successfully
// constructed KeyProvider that implements ManagedResource must reach
// res.Resources. The UPSTREAM env→live-Vault construction of that provider
// (eager Vault Login + transit key read inside
// adaptervault.NewTransitKeyProviderFromEnv) is owned by the adapters/vault
// suite: white-box fakeVaultClient unit tests plus the //go:build integration
// httptest harness. configcore must NOT re-stand-up a fake Vault here (the
// cell-test/adapter isolation rule keeps platform-adapter infra out of
// composition-root unit tests), so the success half is split: adapters/vault
// proves the provider constructs; this test proves configcore routes it.
type fakeManagedKeyProvider struct {
	fakeKeyProvider
	closed bool
}

func (*fakeManagedKeyProvider) Probes() []healthz.Probe { return nil }

func (*fakeManagedKeyProvider) Worker() worker.Worker { return nil }

func (p *fakeManagedKeyProvider) Close(context.Context) error {
	p.closed = true
	return nil
}

// Compile-time assertion: fakeManagedKeyProvider is BOTH a KeyProvider and a
// ManagedResource — matching the real vault-transit provider's dual interface,
// which is what buildConfigCoreResult's type assertion keys on.
var (
	_ kcrypto.KeyProvider             = (*fakeManagedKeyProvider)(nil)
	_ kernellifecycle.ManagedResource = (*fakeManagedKeyProvider)(nil)
)

// TestModule_Provide_KeyProviderImplementingManagedResource_SurfacedInResources
// is the configcore-owned half of the vault-transit success path (F6): when the
// resolved KeyProvider also implements kernel/lifecycle.ManagedResource (as the
// real vault-transit provider does), buildConfigCoreResult must surface it in
// ModuleResult.Resources so Builder.Build registers it for steady-state cleanup
// and pre-Run rollback. This pins the success-path routing without a live or
// fake Vault — the env→Vault construction of the real provider is owned by the
// adapters/vault suite (see fakeManagedKeyProvider godoc).
func TestModule_Provide_KeyProviderImplementingManagedResource_SurfacedInResources(t *testing.T) {
	ctx := context.Background()
	shared := buildMemSharedDeps(t)

	kp := &fakeManagedKeyProvider{}
	res, err := configcore.Module(configcore.WithKeyProviderOverride(kp)).Provide(ctx, shared)
	require.NoError(t, err)
	require.NotNil(t, res.Cell)

	require.Len(t, res.Resources, 1,
		"a KeyProvider that implements ManagedResource (vault-transit shape) must be surfaced in res.Resources for rollback/cleanup")
	assert.Same(t, kp, res.Resources[0],
		"the surfaced ManagedResource must be the resolved KeyProvider itself")
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

	topo, err := bootstrap.NewTopology("", "memory", false)
	require.NoError(t, err)

	shared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                clk,
		Topology:             topo,
		JWTIssuer:            issuer,
		JWTVerifier:          verifier,
		MetricsProvider:      kernelmetrics.NopProvider{},
		Publisher:            eb,
		Subscriber:           eb,
		ConfigEventCollector: obmetrics.NoopConfigEventCollector{},
		ConsumerClaimer:      claimer,
		InternalHMACRing:     ring,
		PrimaryHTTPAddr:      ":8080",
		InternalHTTPAddr:     "127.0.0.1:9090",
		HealthHTTPAddr:       "127.0.0.1:9091",
		VerboseDisabled:      true,
		// ConfigKeyProvider: nil — dev mode uses an explicit NoopTransformer.
		// EventbusCacheCollector / ConfigStaleCipherInc removed (#1413): configcore
		// self-builds them from MetricsProvider (NopProvider here).
	})
	require.NoError(t, err)
	return shared
}
