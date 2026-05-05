package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestInternalAddrToBaseURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "empty address defaults to loopback 9090",
			addr: "",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "port-only resolves to loopback",
			addr: ":9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "port-only non-standard port",
			addr: ":9191",
			want: "http://127.0.0.1:9191",
		},
		{
			name: "explicit loopback address unchanged",
			addr: "127.0.0.1:9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "0.0.0.0:port normalised to 127.0.0.1 (defense against misconfiguration)",
			addr: "0.0.0.0:9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "0.0.0.0:non-standard port normalised",
			addr: "0.0.0.0:9191",
			want: "http://127.0.0.1:9191",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := internalAddrToBaseURL(tt.addr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAccessCoreModule_InvalidAdminProvisionMode_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(AdminProvisionModeEnv, "bootstrp")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Contains(t, ecErr.Message, AdminProvisionModeEnv)
	attr, ok := ecErr.FindAttr("got")
	assert.True(t, ok, "expected 'got' detail attr")
	assert.Equal(t, "bootstrp", attr.Value.String())
}

func TestAccessCoreModule_ForceBootstrapDoesNotMaskInvalidProvisionMode(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(AdminProvisionModeEnv, "bootstrp")

	_, _, _, err := AccessCoreModule{ForceBootstrap: true}.Provide(context.Background(), shared)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Contains(t, ecErr.Message, AdminProvisionModeEnv)
	attr, ok := ecErr.FindAttr("got")
	assert.True(t, ok, "expected 'got' detail attr")
	assert.Equal(t, "bootstrp", attr.Value.String())
}

// --- SEC-SETUP-CLOSURE RED tests (Batch 0, tests 5-9) ---
// These tests target the new SetupModeEnv + loadBootstrapCredentials behavior
// that does not yet exist. They will be GREEN after Batch 2 / Agent-D implements
// the composition-root changes.

// TestAccessCoreModule_EmptyConfig_FailsFast verifies that an empty
// GOCELL_SETUP_MODE environment variable causes Provide to return an error
// with ERR_CELL_INVALID_CONFIG. Currently the module defaults to interactive
// when the env var is empty, so this test is RED.
func TestAccessCoreModule_EmptyConfig_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	// Ensure the new SetupModeEnv is empty (old AdminProvisionModeEnv may still
	// be present in env; unset both to avoid interference).
	t.Setenv(SetupModeEnv, "")
	t.Setenv(AdminProvisionModeEnv, "")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err, "empty GOCELL_SETUP_MODE must fail-fast (RED: current code defaults to interactive)")
	assert.Contains(t, err.Error(), SetupModeEnv,
		"error must reference the env var name for operator diagnostics")
}

// TestAccessCoreModule_InteractiveMissingCredentials_FailsFast verifies that
// selecting interactive mode without GOCELL_BOOTSTRAP_ADMIN_USERNAME/PASSWORD
// set causes a fail-fast error. Currently the module does not check for
// bootstrap credentials in interactive mode, so this test is RED.
func TestAccessCoreModule_InteractiveMissingCredentials_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(SetupModeEnv, "interactive")
	// Ensure bootstrap credentials are NOT set.
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err, "interactive mode without bootstrap credentials must fail-fast (RED)")
	assert.Contains(t, err.Error(), "GOCELL_BOOTSTRAP_ADMIN",
		"error must reference the missing credential env vars")
}

// TestAccessCoreModule_BootstrapMissingCredentials_FailsFast verifies that
// bootstrap mode without both USERNAME and PASSWORD set causes fail-fast.
// Currently the module does not require bootstrap credentials separately from
// the provision mode, so this test is RED.
func TestAccessCoreModule_BootstrapMissingCredentials_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(SetupModeEnv, "bootstrap")
	// Only USERNAME set, PASSWORD missing — XOR violation.
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err, "bootstrap mode with only username set must fail-fast (RED)")
	assert.Contains(t, err.Error(), "GOCELL_BOOTSTRAP_ADMIN",
		"error must reference the missing credential env var")
}

// TestAccessCoreModule_BootstrapWeakPassword_FailsFast verifies that a
// password shorter than 8 bytes causes fail-fast in bootstrap mode.
func TestAccessCoreModule_BootstrapWeakPassword_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(SetupModeEnv, "bootstrap")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "short") // < 8 bytes

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err, "password shorter than 8 bytes must fail-fast (RED)")
}

// TestAccessCoreModule_BootstrapInvalidUsername_FailsFast verifies that a
// username containing control characters causes fail-fast in bootstrap mode.
func TestAccessCoreModule_BootstrapInvalidUsername_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(SetupModeEnv, "bootstrap")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin\x01control") // contains control char
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "securepassword123")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err, "username with control chars must fail-fast (RED)")
}

// TestAccessCoreModule_TrimsEnvWhitespace verifies that K8s-style secrets with
// trailing newlines are trimmed before credential validation. Specifically,
// "admin\n" and "securepassword123\n" should be treated as valid credentials
// after TrimSpace. Currently the module neither validates nor trims credentials,
// so this test is RED: it asserts that Provide with valid trimmed credentials
// does NOT return a credential-validation error — but after Batch 2 implements
// validation, untrimmed "admin\n" would incorrectly be rejected without trimming.
//
// The RED assertion: loadBootstrapCredentials (Batch 2) must exist and trim
// whitespace. This test directly calls the function that doesn't exist yet.
func TestAccessCoreModule_TrimsEnvWhitespace(t *testing.T) {
	// loadBootstrapCredentials does not exist yet in Batch 0 — this test
	// documents its expected behavior and is RED until Batch 2 implements it.
	const username = "admin\n"
	const password = "securepassword123\n"

	creds, err := loadBootstrapCredentials(username, password)
	require.NoError(t, err,
		"trailing newline must be trimmed before validation — loadBootstrapCredentials not yet implemented (RED)")
	assert.Equal(t, []byte("admin"), creds.Username,
		"username must be TrimSpace-d")
	assert.Equal(t, []byte("securepassword123"), creds.Password,
		"password must be TrimSpace-d")
}
