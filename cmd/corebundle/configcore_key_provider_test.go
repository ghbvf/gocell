package main

import (
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// validConfigMasterKey is a 32-byte hex-encoded master key suitable for the
// local-aes provider in dev mode (mirrors runtime/crypto's test key).
const validConfigMasterKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// nilVaultMetrics is the vault-transit metrics factory; the local-aes / empty
// provider paths never invoke it, so a panicking stub proves they don't.
func nilVaultMetrics() (*adaptervault.TransitMetrics, error) {
	panic("vault metrics factory must not be called on non-vault paths")
}

// TestBuildKeyProviderFromName_PostgresEmptyProviderFailsClosed is the fail-closed
// regression (F3): an empty GOCELL_CONFIGCORE_KEY_PROVIDER with StorageBackend=postgres
// must be rejected with ErrConfigKeyMissing — silent NoopTransformer fallback would
// persist sensitive config values unencrypted.
func TestBuildKeyProviderFromName_PostgresEmptyProviderFailsClosed(t *testing.T) {
	kp, err := buildKeyProviderFromName("postgres", "", "", "", "", clock.Real(), nilVaultMetrics)

	require.Error(t, err)
	assert.Nil(t, kp)
	assertErrCode(t, err, errcode.ErrConfigKeyMissing)
}

// TestBuildKeyProviderFromName_MemoryEmptyProviderReturnsNil verifies the
// documented no-key sentinel: empty provider in non-postgres mode returns
// (nil, nil) so cellmodules/configcore falls through to NoopTransformer.
func TestBuildKeyProviderFromName_MemoryEmptyProviderReturnsNil(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", "", "", "", "", clock.Real(), nilVaultMetrics)

	require.NoError(t, err)
	assert.Nil(t, kp, "memory + empty provider is the no-key sentinel")
}

// TestBuildKeyProviderFromName_UnknownProviderRejected verifies an unrecognized
// provider name is rejected with ErrValidationFailed.
func TestBuildKeyProviderFromName_UnknownProviderRejected(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", "", "bogus-provider", "", "", clock.Real(), nilVaultMetrics)

	require.Error(t, err)
	assert.Nil(t, kp)
	assertErrCode(t, err, errcode.ErrValidationFailed)
}

// TestBuildKeyProviderFromName_LocalAES verifies the local-aes happy path builds
// a non-nil KeyProvider from valid key material in dev mode.
func TestBuildKeyProviderFromName_LocalAES(t *testing.T) {
	kp, err := buildKeyProviderFromName("postgres", "", "local-aes", validConfigMasterKey, "", clock.Real(), nilVaultMetrics)

	require.NoError(t, err)
	assert.NotNil(t, kp)
}

// TestBuildStaleCipherInc_NilRegistryNoop verifies the stale-cipher callback is
// always non-nil and is a safe silent no-op when no registry is supplied.
func TestBuildStaleCipherInc_NilRegistryNoop(t *testing.T) {
	inc, err := buildStaleCipherInc(nil)

	require.NoError(t, err)
	require.NotNil(t, inc)
	assert.NotPanics(t, inc, "nil-registry callback must be a safe no-op")
}

// TestBuildStaleCipherInc_RegistersAndCounts is the stale-cipher registration
// regression (F3): with a registry the counter must be registered and the
// returned callback must increment the gocell_config_stale_cipher_total series.
func TestBuildStaleCipherInc_RegistersAndCounts(t *testing.T) {
	registry := prom.NewRegistry()

	inc, err := buildStaleCipherInc(registry)
	require.NoError(t, err)
	require.NotNil(t, inc)

	inc()
	inc()

	assert.InDelta(t, 2.0, gatherCounterValue(t, registry, "gocell_config_stale_cipher_total"), 1e-9,
		"stale-cipher callback must increment the registered counter")
}

// gatherCounterValue gathers registry and returns the value of the named counter
// metric family. Fails the test if the family is absent.
func gatherCounterValue(t *testing.T, registry *prom.Registry, name string) float64 {
	t.Helper()
	mfs, err := registry.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		metrics := mf.GetMetric()
		require.NotEmpty(t, metrics, "metric family %q has no samples", name)
		return metrics[0].GetCounter().GetValue()
	}
	t.Fatalf("metric family %q not found in registry (counter not registered)", name)
	return 0
}
