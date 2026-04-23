package main

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSharedDeps_Validate_PostgresWithoutKeyProvider_OK verifies that
// SharedDeps.Validate() no longer checks KeyProvider presence — that check
// was moved to ConfigCoreModule.Provide (per-cell responsibility).
// A postgres SharedDeps with no key-provider field should still pass Validate().
func TestSharedDeps_Validate_PostgresWithoutKeyProvider_OK(t *testing.T) {
	// Build a minimal postgres SharedDeps; other required fields are nil,
	// so Validate will still error, but NOT for the key-provider check.
	deps := &SharedDeps{
		Topology: bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		// Deliberately leave other fields zero to isolate the specific check.
	}

	err := deps.Validate()
	// Deps fields are not fully populated, so Validate must return an error.
	require.Error(t, err, "minimal SharedDeps must fail Validate due to missing required fields")
	assert.NotContains(t, err.Error(), "GOCELL_CONFIGCORE_KEY_PROVIDER",
		"SharedDeps.Validate must not check key provider — that is ConfigCoreModule.Provide's job")
	assert.NotContains(t, err.Error(), "GOCELL_KEY_PROVIDER",
		"old env name must not appear in SharedDeps.Validate")
}

// TestSharedDeps_Validate_MemoryTopology_OK verifies that memory mode doesn't
// require any key-provider configuration.
func TestSharedDeps_Validate_MemoryTopology_OK(t *testing.T) {
	deps := &SharedDeps{
		Topology: bootstrap.Topology{StorageBackend: "memory", AdapterMode: "dev"},
	}

	err := deps.Validate()
	// Deps fields are not fully populated, so Validate must return an error.
	require.Error(t, err, "minimal SharedDeps must fail Validate due to missing required fields")
	assert.NotContains(t, err.Error(), "GOCELL_KEY_PROVIDER")
	assert.NotContains(t, err.Error(), "GOCELL_CONFIGCORE_KEY_PROVIDER")
}

// TestSharedDeps_Validate_NilReceiver_Errors verifies the defensive nil check.
func TestSharedDeps_Validate_NilReceiver_Errors(t *testing.T) {
	var deps *SharedDeps
	err := deps.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil receiver")
}
