package main

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSharedDeps_Validate_PostgresWithoutKeyProvider_Fails verifies that
// SharedDeps.Validate() returns an error when StorageBackend=postgres but
// GOCELL_KEY_PROVIDER is unset. This is defense-in-depth: buildKeyProvider
// also checks this, but Validate catches test-constructed SharedDeps.
func TestSharedDeps_Validate_PostgresWithoutKeyProvider_Fails(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "")

	deps := &SharedDeps{
		Topology: bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		// Deliberately leave other fields zero to isolate this check.
		// Validate will also report other missing fields, but we just need
		// to find our specific error in the joined result.
	}

	err := deps.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_KEY_PROVIDER")
}

// TestSharedDeps_Validate_MemoryWithoutKeyProvider_OK verifies that
// memory mode doesn't require GOCELL_KEY_PROVIDER.
func TestSharedDeps_Validate_MemoryWithoutKeyProvider_OK(t *testing.T) {
	t.Setenv("GOCELL_KEY_PROVIDER", "")

	// Build a minimal SharedDeps for memory mode.
	// This test only asserts that the KeyProvider check doesn't fire;
	// other fields will still be missing, so Validate will still error.
	// We check that the specific "GOCELL_KEY_PROVIDER" message is NOT in the error.
	deps := &SharedDeps{
		Topology: bootstrap.Topology{StorageBackend: "memory", AdapterMode: "dev"},
	}

	err := deps.Validate()
	// Will error for other missing fields, but NOT for KeyProvider.
	if err != nil {
		assert.NotContains(t, err.Error(), "GOCELL_KEY_PROVIDER")
	}
}
