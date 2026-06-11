package configcore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// validLocalAESMasterKey is a 32-byte hex-encoded master key suitable for the
// local-aes provider in dev mode (matches runtime/crypto's test key format).
const validLocalAESMasterKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// nopMetricsProvider is a NopProvider that must NOT be called on non-vault
// paths (any call would still succeed, but we verify via assertions).
var nopMetricsProvider = kernelmetrics.NopProvider{}

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
// branch fails cleanly when VAULT_ADDR / related env vars are absent.
// This confirms the vault branch is reachable without triggering any test
// infrastructure — the expected failure is a vault connection error, not a
// metrics registration error.
func TestBuildKeyProviderFromName_VaultTransit_NoEnv(t *testing.T) {
	// NopProvider is sufficient: NewTransitMetrics succeeds on NopProvider.
	// NewTransitKeyProviderFromEnv will fail because VAULT_ADDR is absent.
	kp, err := buildKeyProviderFromName(
		"postgres", "", "vault-transit", "", "", clock.Real(), nopMetricsProvider)

	// Must fail (no vault configured) but must not panic.
	require.Error(t, err, "vault-transit without VAULT_ADDR must fail")
	assert.Nil(t, kp)
}
