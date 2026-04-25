package main

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
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
		{
			// Regression for the PR-A35 review-3 P1: `cp .env.example .env`
			// + `s/POSTGRES_PASSWORD/.../` left the public sample verbose
			// token in place; without this guard a real-mode deploy that
			// rotated only the database secrets would still ship with a
			// repo-known token gating /readyz?verbose.
			name:       "prod mode rejects the .env.example sample verbose token",
			topo:       prodTopo,
			mutate:     func(d *SharedDeps) { d.VerboseToken = SampleVerboseToken; d.VerboseDisabled = false },
			wantErr:    true,
			wantSubstr: "GOCELL_READYZ_VERBOSE_TOKEN is set to the .env.example placeholder",
		},
		{
			name:    "dev mode permits the sample verbose token (out-of-the-box demo path)",
			topo:    devTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = SampleVerboseToken; d.VerboseDisabled = false },
			wantErr: false,
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

type validateDistributedNonceStore struct{}

func (validateDistributedNonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKindDistributed
}

func (validateDistributedNonceStore) CheckAndMark(context.Context, string) error {
	return nil
}

func TestSharedDeps_Validate_RealMultiPodRejectsInMemoryClaimerCode(t *testing.T) {
	topo := bootstrap.Topology{
		StorageBackend:            "postgres",
		AdapterMode:               "real",
		SinglePodReplayProtection: false,
	}
	deps := newValidatedSharedDeps(t, topo)
	deps.InternalGuard.nonceStore = validateDistributedNonceStore{}
	deps.ConsumerClaimerKind = consumerClaimerKindInMemory

	err := deps.Validate()

	require.Error(t, err)
	leaves := allJoinedErrors(err)
	require.Len(t, leaves, 1)

	var ec *errcode.Error
	require.ErrorAs(t, leaves[0], &ec)
	assert.Equal(t, errcode.ErrControlplaneClaimerNotDistributed, ec.Code)
	assert.Contains(t, ec.Error(), "ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED")
}
