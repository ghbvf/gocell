package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
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

// TestBootstrapAuthFailLogger_RecordsClientIP verifies that bootstrapAuthFailLogger
// writes a slog record containing the "client_ip" field when the context carries
// a real IP. This is the F2 RED test — it fails until the observer calls
// ctxkeys.RealIPFrom and logs slog.String("client_ip", ip).
func TestBootstrapAuthFailLogger_RecordsClientIP(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	// Replace the default slog logger for this test.
	// bootstrapAuthFailLogger uses slog.ErrorContext which goes through the default logger.
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(orig) })

	observer := bootstrapAuthFailLogger()
	ctx := ctxkeys.WithRealIP(context.Background(), "192.0.2.1")
	observer(ctx, "rate_limited")

	logged := buf.String()
	assert.True(t, strings.Contains(logged, "client_ip=192.0.2.1"),
		"bootstrapAuthFailLogger must log client_ip field; got: %q", logged)
}

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
