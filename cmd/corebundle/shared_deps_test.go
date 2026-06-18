package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// TestCompositionSharedDeps_NewSharedDeps_PostgresWithoutKeyProvider verifies that
// composition.NewSharedDeps does not check KeyProvider presence — that check
// lives in cellmodules/configcore.Module.Provide (per-cell responsibility).
func TestCompositionSharedDeps_NewSharedDeps_PostgresWithoutKeyProvider(t *testing.T) {
	// Build a minimal composition.SharedDeps; other required fields are nil,
	// so NewSharedDeps will still error, but NOT for the key-provider check.
	_, err := composition.NewSharedDeps(composition.SharedDeps{
		Topology: mkTopo("real", "postgres", false),
		// Deliberately leave other fields zero to isolate the specific check.
	})
	require.Error(t, err, "minimal SharedDeps must fail validation due to missing required fields")
	assert.NotContains(t, err.Error(), "GOCELL_CONFIGCORE_KEY_PROVIDER",
		"NewSharedDeps must not check key provider — that is configcore.Module.Provide's job")
	assert.NotContains(t, err.Error(), "GOCELL_KEY_PROVIDER",
		"old env name must not appear in NewSharedDeps validation")
}

// TestCompositionSharedDeps_NewSharedDeps_MemoryTopology verifies that memory mode
// does not require any key-provider configuration.
func TestCompositionSharedDeps_NewSharedDeps_MemoryTopology(t *testing.T) {
	_, err := composition.NewSharedDeps(composition.SharedDeps{
		Topology: mkTopo("", "memory", false),
	})
	require.Error(t, err, "minimal SharedDeps must fail validation due to missing required fields")
	assert.NotContains(t, err.Error(), "GOCELL_KEY_PROVIDER")
	assert.NotContains(t, err.Error(), "GOCELL_CONFIGCORE_KEY_PROVIDER")
}

// TestValidateCorebundleDeps_VerboseEndpoint is a focused table-driven test for
// the verbose endpoint invariant — composition.SharedDeps.validate rejects any
// SharedDeps that has no verbose token and has not explicitly waived the
// endpoint. The sample-placeholder CP2 check (cmd-only) is also covered here.
func TestValidateCorebundleDeps_VerboseEndpoint(t *testing.T) {
	prodTopo := mkTopo("real", "postgres", true)
	devTopo := mkTopo("", "memory", false)

	tests := []struct {
		name         string
		topo         bootstrap.Topology
		mutateShared func(*composition.SharedDeps)
		wantErr      bool
		wantSubstr   string
	}{
		{
			name:         "dev mode with token is valid",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = "unit-test-verbose"; d.VerboseDisabled = false },
			wantErr:      false,
		},
		{
			name:         "dev mode with VerboseDisabled is valid",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = ""; d.VerboseDisabled = true },
			wantErr:      false,
		},
		{
			name:         "prod mode with token is valid",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = "unit-test-verbose"; d.VerboseDisabled = false },
			wantErr:      false,
		},
		{
			name:         "prod mode rejects the .env.example sample verbose token (CP2)",
			topo:         prodTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder; d.VerboseDisabled = false },
			wantErr:      true, wantSubstr: "ERR_CONTROLPLANE_VERBOSE_TOKEN_SAMPLE",
		},
		{
			name:         "dev mode permits the sample verbose token (out-of-the-box demo path)",
			topo:         devTopo,
			mutateShared: func(d *composition.SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder; d.VerboseDisabled = false },
			wantErr:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shared, _ := newValidatedSharedDepsAndLocals(t, tc.topo)
			tc.mutateShared(shared)
			err := validateCorebundleDeps(shared)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}

func TestLoadSharedDepsFromEnv_RealModeAllowsDefaultLoopbackHealthWithLocalOnlyWaiver(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
	t.Setenv("GOCELL_HTTP_HEALTH_ADDR", "")
	t.Setenv("GOCELL_HTTP_HEALTH_LOCAL_ONLY", "1")
	t.Setenv("GOCELL_SINGLE_POD", "1")
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
	t.Setenv("GOCELL_METRICS_TOKEN", "metrics-token-present")

	compShared, _, err := LoadSharedDepsFromEnv(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9091", compShared.HealthHTTPAddr)
	assert.True(t, compShared.HealthLocalOnly)
}

// TestLoadSharedDepsFromEnv_NonEmptyCellRoleFailsClosed verifies the #2278 F1 fix:
// the composition root reads GOCELL_CELL_ROLE and forwards it to
// bootstrap.SpecForRole. PR-1 supports only the all-colocated monolith, so a
// non-empty role must fail closed at startup (ERR_VALIDATION_FAILED) rather than
// be silently ignored (which would mount the full monolith while the operator
// believes they selected a split role).
func TestLoadSharedDepsFromEnv_NonEmptyCellRoleFailsClosed(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
	t.Setenv("GOCELL_HTTP_HEALTH_ADDR", "")
	t.Setenv("GOCELL_HTTP_HEALTH_LOCAL_ONLY", "1")
	t.Setenv("GOCELL_SINGLE_POD", "1")
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
	t.Setenv("GOCELL_METRICS_TOKEN", "metrics-token-present")
	// The role selector that PR-1 does not yet support → must fail closed.
	t.Setenv("GOCELL_CELL_ROLE", "core")

	_, _, err := LoadSharedDepsFromEnv(context.Background())
	require.Error(t, err, "a non-empty GOCELL_CELL_ROLE must fail closed on a PR-1 build")
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED",
		"fail-closed role error must surface SpecForRole's ERR_VALIDATION_FAILED")
}
