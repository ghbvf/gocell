package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/state/cas"
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

// The former TestBootstrapAuthFailLogger_RecordsClientIP unit test has moved
// to runtime/audit/bootstrap_observer_test.go. The replacement
// TestNewBootstrapAuthFailObserver_DoubleWriteSlogAndAudit now owns both the
// slog client_ip assertion and the audit hash-chain assertion in one place,
// matching the wiring funnel collapsed under BOOTSTRAP-AUDIT-CHAIN-WIRING-01
// (plan 039 W1-2). Static enforcement that this composition root reaches the
// funnel lives in tools/archtest/bootstrap_audit_observer_funnel_test.go
// (BOOTSTRAP-AUDIT-OBSERVER-FUNNEL-UPSTREAM-MEDIUM-01).

// TestAccessCoreModule_BootstrapMissingCredentials_FailsFast verifies that
// Provide returns an error when only USERNAME is set but PASSWORD is missing (XOR violation).
func TestAccessCoreModule_BootstrapMissingCredentials_FailsFast(t *testing.T) {
	// Only USERNAME set, PASSWORD missing — XOR violation.
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "")

	_, err := loadBootstrapCredentials("admin", "")
	require.Error(t, err, "only username set must fail-fast")
	assert.Contains(t, err.Error(), "GOCELL_BOOTSTRAP_ADMIN",
		"error must reference the missing credential env var")
}

// TestAccessCoreModule_BootstrapWeakPassword_FailsFast verifies that a
// password shorter than 8 bytes causes fail-fast.
func TestAccessCoreModule_BootstrapWeakPassword_FailsFast(t *testing.T) {
	_, err := loadBootstrapCredentials("admin", "short")
	require.Error(t, err, "password shorter than 8 bytes must fail-fast")
}

// TestAccessCoreModule_BootstrapInvalidUsername_FailsFast verifies that a
// username containing control characters causes fail-fast.
func TestAccessCoreModule_BootstrapInvalidUsername_FailsFast(t *testing.T) {
	_, err := loadBootstrapCredentials("admin\x01control", "securepassword123")
	require.Error(t, err, "username with control chars must fail-fast")
}

// TestAccessCoreModule_TrimsEnvWhitespace verifies that K8s-style secrets with
// trailing newlines are trimmed before credential validation.
func TestAccessCoreModule_TrimsEnvWhitespace(t *testing.T) {
	const username = "admin\n"
	const password = "securepassword123\n"

	creds, err := loadBootstrapCredentials(username, password)
	require.NoError(t, err,
		"trailing newline must be trimmed before validation")
	assert.Equal(t, []byte("admin"), creds.Username,
		"username must be TrimSpace-d")
	assert.Equal(t, []byte("securepassword123"), creds.Password,
		"password must be TrimSpace-d")
}

// TestAccessCoreModule_CASProtocolInjection verifies that the composition root
// constructs a valid CAS Protocol with password_version as version field.
// This test documents the expected invariant without running the full cell
// bootstrap (which would require JWT keys, DB, etc.).
func TestAccessCoreModule_CASProtocolInjection(t *testing.T) {
	// MustNewProtocol panics on misconfiguration. The fact that this does not
	// panic proves the parameters are valid. The composition root calls exactly
	// this constructor form (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest guards
	// that cells never call it directly).
	proto := mustNewCASProtocol(t, "password_version")
	require.NotNil(t, proto, "CAS Protocol must be non-nil")
	assert.Equal(t, "password_version", proto.VersionField(),
		"CAS Protocol version field must match the DB column name from migration 022")
	_, isStrictReject := proto.Conflict().(cas.ConflictPolicyStrictReject)
	assert.True(t, isStrictReject,
		"default conflict policy must be ConflictPolicyStrictReject (HTTP 409 on mismatch)")
}

func mustNewCASProtocol(t *testing.T, versionField string) *cas.Protocol {
	t.Helper()
	p, err := cas.NewProtocol(cas.WithVersionField(versionField))
	if err != nil {
		t.Fatalf("cas.NewProtocol: %v", err)
	}
	return p
}
