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

// TestSharedDeps_Validate_VerboseEndpoint is a focused table-driven test for
// PR-A35's new invariant — `validateVerboseEndpoint` must reject any
// SharedDeps that has no verbose token and has not explicitly waived the
// endpoint. The prod-mode-disabled path is also asserted here so the
// security contract is covered end-to-end outside the broader
// TestSharedDeps_Validate helper.
func TestSharedDeps_Validate_VerboseEndpoint(t *testing.T) {
	prodTopo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: true}
	devTopo := bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""}

	tests := []struct {
		name       string
		topo       bootstrap.Topology
		mutate     func(d *SharedDeps) // applied on top of newValidatedSharedDeps baseline
		wantErr    bool
		wantSubstr string
	}{
		{
			name:    "dev mode with token is valid",
			topo:    devTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = "unit-test-verbose"; d.VerboseDisabled = false },
			wantErr: false,
		},
		{
			name:    "dev mode with VerboseDisabled is valid",
			topo:    devTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = true },
			wantErr: false,
		},
		{
			name:       "dev mode with neither token nor disabled is rejected",
			topo:       devTopo,
			mutate:     func(d *SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = false },
			wantErr:    true,
			wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN must be set",
		},
		{
			name:    "prod mode with token is valid",
			topo:    prodTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = "unit-test-verbose"; d.VerboseDisabled = false },
			wantErr: false,
		},
		{
			name:       "prod mode cannot waive verbose via VerboseDisabled",
			topo:       prodTopo,
			mutate:     func(d *SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = true },
			wantErr:    true,
			wantSubstr: "GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := newValidatedSharedDeps(t, tc.topo)
			tc.mutate(deps)
			err := deps.Validate()
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}
