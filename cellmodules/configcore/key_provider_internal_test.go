package configcore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// validLocalAESMasterKey is a 32-byte hex-encoded master key suitable for the
// local-aes provider in dev mode (matches runtime/crypto's test key format).
const validLocalAESMasterKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// nopMetricsProvider is a NopProvider that must NOT be called on non-vault
// paths (any call would still succeed, but we verify via assertions).
var nopMetricsProvider = kernelmetrics.NopProvider{}

// recordingMetricsProvider wraps NopProvider and records the Name of every
// CounterVec/GaugeVec registration. It is the seam that proves metric
// registration LOCALITY without a real registry or network: only the
// vault-transit branch constructs adapters/vault.TransitMetrics, so only that
// branch may touch the provider. The embedded NopProvider supplies the rest of
// the metrics.Provider surface (HistogramVec / Unregister) and returns working
// nop instruments, so NewTransitMetrics' .With(Labels{}) calls still succeed.
type recordingMetricsProvider struct {
	kernelmetrics.NopProvider
	names []string
}

func (p *recordingMetricsProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	p.names = append(p.names, opts.Name)
	return p.NopProvider.CounterVec(opts)
}

func (p *recordingMetricsProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	p.names = append(p.names, opts.Name)
	return p.NopProvider.GaugeVec(opts)
}

// TestBuildKeyProviderFromName_PostgresEmptyProviderFailsClosed is the
// fail-closed regression (F3): an empty GOCELL_CONFIGCORE_KEY_PROVIDER with
// StorageBackend=postgres must be rejected with ErrConfigKeyMissing — silent
// NoopTransformer fallback would persist sensitive config values unencrypted.
func TestBuildKeyProviderFromName_PostgresEmptyProviderFailsClosed(t *testing.T) {
	kp, err := buildKeyProviderFromName(
		"postgres", "", "", "", "", clock.Real(), nopMetricsProvider)

	require.Error(t, err)
	assert.Nil(t, kp)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrConfigKeyMissing, ecErr.Code)
}

// TestBuildKeyProviderFromName_MemoryEmptyProviderReturnsNil verifies the
// documented no-key sentinel: empty provider in non-postgres mode returns
// (nil, nil) so configcore resolveValueTransformer falls through to NoopTransformer.
func TestBuildKeyProviderFromName_MemoryEmptyProviderReturnsNil(t *testing.T) {
	kp, err := buildKeyProviderFromName(
		"memory", "", "", "", "", clock.Real(), nopMetricsProvider)

	require.NoError(t, err)
	assert.Nil(t, kp, "memory + empty provider is the no-key sentinel")
}

// TestBuildKeyProviderFromName_UnknownProviderRejected verifies an unrecognized
// provider name is rejected with ErrValidationFailed.
func TestBuildKeyProviderFromName_UnknownProviderRejected(t *testing.T) {
	kp, err := buildKeyProviderFromName(
		"memory", "", "bogus-provider", "", "", clock.Real(), nopMetricsProvider)

	require.Error(t, err)
	assert.Nil(t, kp)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

// TestBuildKeyProviderFromName_LocalAES verifies the local-aes happy path builds
// a non-nil KeyProvider from valid key material in dev mode.
func TestBuildKeyProviderFromName_LocalAES(t *testing.T) {
	kp, err := buildKeyProviderFromName(
		"postgres", "", "local-aes", validLocalAESMasterKey, "", clock.Real(), nopMetricsProvider)

	require.NoError(t, err)
	assert.NotNil(t, kp)
}

// TestBuildKeyProviderFromName_LocalAES_EmptyMasterKey verifies that an empty
// master key for local-aes is rejected (the underlying AES key provider requires
// non-empty key material).
func TestBuildKeyProviderFromName_LocalAES_EmptyMasterKey(t *testing.T) {
	kp, err := buildKeyProviderFromName(
		"memory", "", "local-aes", "", "", clock.Real(), nopMetricsProvider)

	require.Error(t, err)
	assert.Nil(t, kp)
}

// TestBuildKeyProviderFromName_VaultTransit_NoEnv verifies that the vault-transit
// branch fails cleanly when VAULT_ADDR is absent, AND that the failure is the
// adapter's own VAULT_ADDR-required guard (ErrVaultAuthFailed). That error code
// is minted ONLY inside adaptervault.NewTransitKeyProviderFromEnv, so observing
// it back here positively proves the configcore vault-transit branch delegates
// all the way THROUGH NewTransitMetrics and INTO the real adapter env
// constructor — it does not stop at metrics registration. This is the
// delegation-locality half of the vault-transit success-path wiring (F6); the
// success half past this eager-I/O boundary (a usable provider) is owned by the
// adapters/vault suite (white-box fakeVaultClient unit + //go:build integration
// httptest), and the configcore routing of a successful provider into
// res.Resources is pinned by
// TestModule_Provide_KeyProviderImplementingManagedResource_SurfacedInResources.
func TestBuildKeyProviderFromName_VaultTransit_NoEnv(t *testing.T) {
	// Hermetic: force VAULT_ADDR empty regardless of the developer's shell so the
	// branch deterministically reaches the adapter's VAULT_ADDR-required guard
	// (rather than a TLS / client-creation / Login failure with a stray addr).
	t.Setenv("VAULT_ADDR", "")

	// NopProvider is sufficient: NewTransitMetrics succeeds on NopProvider.
	// NewTransitKeyProviderFromEnv then fails at the VAULT_ADDR-required guard.
	kp, err := buildKeyProviderFromName(
		"postgres", "", providerVaultTransit, "", "", clock.Real(), nopMetricsProvider)

	// Must fail (no vault configured) but must not panic.
	require.Error(t, err, "vault-transit without VAULT_ADDR must fail")
	assert.Nil(t, kp)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrVaultAuthFailed, ecErr.Code,
		"failure must be the adapter's VAULT_ADDR-required guard, proving the "+
			"vault-transit branch delegates into NewTransitKeyProviderFromEnv")
}

// TestBuildKeyProviderFromName_VaultTransit_NilMetricsProvider verifies that
// passing a nil metrics.Provider for vault-transit returns an error (from the
// NewTransitMetrics nil-guard) without panicking. The nil-provider guard fires
// before any vault network access, so no VAULT_ADDR is needed.
func TestBuildKeyProviderFromName_VaultTransit_NilMetricsProvider(t *testing.T) {
	var nilProvider kernelmetrics.Provider // typed nil

	kp, err := buildKeyProviderFromName(
		"postgres", "", "vault-transit", "", "", clock.Real(), nilProvider)

	require.Error(t, err, "vault-transit with nil metrics.Provider must return an error")
	assert.Nil(t, kp)
	var ecErr *errcode.Error
	if assert.ErrorAs(t, err, &ecErr) {
		assert.Equal(t, errcode.ErrInternal, ecErr.Code,
			"nil-provider error must carry ErrInternal")
	}
}

// TestBuildKeyProviderFromName_VaultTransit_RegistersVaultMetrics is the
// metric-registration-locality success half (F6): the vault-transit branch
// self-builds adapters/vault.TransitMetrics, registering the five vault transit
// instruments on the shared provider, BEFORE it attempts the (here failing)
// Vault connection. The connection failure is expected and irrelevant — the
// assertion is that the vault branch IS the branch that registers gocell_vault_*
// series. The full real-Vault success path (usable KeyProvider) is covered by
// the adapters/vault integration suite; this unit test pins the wiring locality
// without network or a fake-Vault seam.
func TestBuildKeyProviderFromName_VaultTransit_RegistersVaultMetrics(t *testing.T) {
	rp := &recordingMetricsProvider{}

	_, err := buildKeyProviderFromName(
		"postgres", "", providerVaultTransit, "", "", clock.Real(), rp)
	require.Error(t, err, "vault-transit without VAULT_ADDR must fail after metric registration")

	assert.ElementsMatch(t, []string{
		"vault_token_renew_success_total",
		"vault_token_renew_failure_total",
		"vault_token_auth_healthy",
		"vault_auth_login_total",
		"vault_cached_key_version",
	}, rp.names,
		"vault-transit branch must register exactly the five vault transit instruments")
}

// TestBuildKeyProviderFromName_LocalAES_RegistersNoMetrics is the
// metric-registration-locality complement (F6): the local-aes branch must NOT
// touch the metrics provider at all — registering gocell_vault_* series on a
// non-vault deployment would be a false signal. Together with the vault-transit
// test above this proves the self-build is branch-local.
func TestBuildKeyProviderFromName_LocalAES_RegistersNoMetrics(t *testing.T) {
	rp := &recordingMetricsProvider{}

	kp, err := buildKeyProviderFromName(
		"postgres", "", providerLocalAES, validLocalAESMasterKey, "", clock.Real(), rp)
	require.NoError(t, err)
	assert.NotNil(t, kp)
	assert.Empty(t, rp.names,
		"local-aes branch must never register vault_* metrics (registration locality)")
}
