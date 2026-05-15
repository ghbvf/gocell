package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/errutil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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
			mutate:     func(d *SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder; d.VerboseDisabled = false },
			wantErr:    true,
			wantSubstr: "ERR_CONTROLPLANE_VERBOSE_TOKEN_SAMPLE",
		},
		{
			name:    "dev mode permits the sample verbose token (out-of-the-box demo path)",
			topo:    devTopo,
			mutate:  func(d *SharedDeps) { d.VerboseToken = SampleVerbosePlaceholder; d.VerboseDisabled = false },
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

func TestSharedDeps_Validate_RealModeRejectsLoopbackHealthAddrWithoutLocalOnlyWaiver(t *testing.T) {
	prodTopo := bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: true}

	tests := []struct {
		name            string
		addr            string
		healthLocalOnly bool
		wantErr         bool
	}{
		{name: "loopback rejected", addr: "127.0.0.1:9091", wantErr: true},
		{name: "localhost rejected", addr: "localhost:9091", wantErr: true},
		{name: "ipv6 loopback rejected", addr: "[::1]:9091", wantErr: true},
		{name: "wildcard accepted", addr: ":9091"},
		{name: "zero wildcard accepted", addr: "0.0.0.0:9091"},
		{name: "pod reachable accepted", addr: "10.0.0.12:9091"},
		{name: "loopback accepted with explicit local-only waiver", addr: "127.0.0.1:9091", healthLocalOnly: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := newValidatedSharedDeps(t, prodTopo)
			deps.HealthHTTPAddr = tc.addr
			deps.HealthLocalOnly = tc.healthLocalOnly

			err := deps.Validate()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GOCELL_HTTP_HEALTH_ADDR")
			assert.Contains(t, err.Error(), "GOCELL_HTTP_HEALTH_LOCAL_ONLY=1")
		})
	}
}

func TestSharedDeps_Validate_InternalListenerRequiresAddrAndGuardInAllModes(t *testing.T) {
	tests := []struct {
		name string
		topo bootstrap.Topology
	}{
		{name: "dev", topo: bootstrap.Topology{StorageBackend: "memory", AdapterMode: ""}},
		{name: "real", topo: bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real", SinglePodReplayProtection: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := newValidatedSharedDeps(t, tc.topo)

			deps.InternalHTTPAddr = ""
			err := deps.Validate()

			require.Error(t, err)
			assert.Contains(t, err.Error(), "InternalHTTPAddr")
			assert.Contains(t, err.Error(), "must be set")

			deps = newValidatedSharedDeps(t, tc.topo)
			deps.InternalHTTPAddr = "127.0.0.1:9090"
			deps.InternalGuard = nil

			err = deps.Validate()

			require.Error(t, err)
			assert.Contains(t, err.Error(), "InternalGuard")
			assert.Contains(t, err.Error(), "/internal/v1/*")
			assert.NotContains(t, err.Error(), "clear GOCELL_HTTP_INTERNAL_ADDR")
		})
	}
}

func TestSharedDeps_Validate_RealMultiPodInMemoryNonceStore_LogsWarnAndErrors(t *testing.T) {
	topo := bootstrap.Topology{
		StorageBackend:            "postgres",
		AdapterMode:               "real",
		SinglePodReplayProtection: false,
	}
	deps := newValidatedSharedDeps(t, topo)

	buf, restore := captureSlogWarnLines(t)
	t.Cleanup(restore)

	err := deps.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SINGLE_POD=1")
	out := buf.String()
	assert.Contains(t, out, "in-memory nonce store rejected")
	assert.Contains(t, out, "multi-pod")
	assert.Contains(t, out, "nonce_store_kind")
	assert.Contains(t, out, string(auth.NonceStoreKindInMemory))
	assert.Contains(t, out, "GOCELL_SINGLE_POD=1")
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

	deps, err := LoadSharedDepsFromEnv(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9091", deps.HealthHTTPAddr)
	assert.True(t, deps.HealthLocalOnly)
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
	leaves := errutil.FlattenJoined(err)
	require.Len(t, leaves, 1)

	var ec *errcode.Error
	require.ErrorAs(t, leaves[0], &ec)
	assert.Equal(t, errcode.ErrControlplaneClaimerNotDistributed, ec.Code)
	assert.Contains(t, ec.Error(), "ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED")
}

// TestIsLoopbackBindAddr table-drives the address parser used by
// validateHealthReachability. Edge cases (empty, IPv6 bracketed, hostname)
// were not previously regression-locked despite the function gating a
// production safety check.
func TestIsLoopbackBindAddr(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"ipv4 loopback with port", "127.0.0.1:8080", true},
		{"ipv4 loopback bare", "127.0.0.1", true},
		{"ipv4 loopback alt range", "127.0.0.5:9090", true},
		{"ipv6 loopback bracketed", "[::1]:8080", true},
		{"ipv6 loopback bracketed bare", "[::1]", true},
		{"ipv6 loopback unbracketed", "::1", true},
		{"hostname localhost lowercase", "localhost:9090", true},
		{"hostname localhost mixed case", "LocalHost:9090", true},
		{"port-only colon means all interfaces", ":8080", false},
		{"empty string", "", false},
		{"public ipv4", "8.8.8.8:80", false},
		{"private ipv4", "10.0.0.5:8080", false},
		{"unspecified ipv4", "0.0.0.0:8080", false},
		{"unspecified ipv6", "[::]:8080", false},
		{"bare hostname not localhost", "example.com:443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isLoopbackBindAddr(tc.addr)
			assert.Equal(t, tc.want, got, "addr=%q", tc.addr)
		})
	}
}
