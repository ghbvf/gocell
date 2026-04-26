package secutil_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
)

// TestValidateTLSEndpoint verifies the TLS-endpoint validation helper across
// the canonical cases defined by the SEC-FAIL-CLOSED loopback exception rule:
//   - Remote endpoints require a TLS scheme (https, rediss, tls-aware bare host is rejected).
//   - Loopback hosts (127.x.x.x, ::1, IPv4-mapped IPv6, localhost) are exempt regardless of scheme.
//   - Empty endpoint is always rejected.
//   - unix:// with empty host is accepted; unix://host/path is rejected.
func TestValidateTLSEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "https remote — ok",
			input:   "https://prod.example.com",
			wantErr: false,
		},
		{
			name:    "http remote — reject",
			input:   "http://prod.example.com",
			wantErr: true,
		},
		{
			name:    "redis loopback — ok",
			input:   "redis://localhost:6379",
			wantErr: false,
		},
		{
			name:    "redis remote — reject",
			input:   "redis://prod.redis:6379",
			wantErr: true,
		},
		{
			name:    "rediss remote — ok",
			input:   "rediss://prod.redis:6379",
			wantErr: false,
		},
		{
			name:    "bare 127.0.0.1:port — ok (loopback exception)",
			input:   "127.0.0.1:6379",
			wantErr: false,
		},
		{
			name:    "bare ::1 — ok (loopback exception)",
			input:   "::1",
			wantErr: false,
		},
		{
			name:    "bare localhost:port — ok (loopback exception)",
			input:   "localhost:8200",
			wantErr: false,
		},
		{
			name:    "bare non-loopback host:port — reject",
			input:   "vault.prod.io:8200",
			wantErr: true,
		},
		{
			name:    "empty string — reject",
			input:   "",
			wantErr: true,
		},
		// IPv6 bracket form with port in URL host.
		{
			name:    "URL with [::1]:port loopback host — ok",
			input:   "redis://[::1]:6379",
			wantErr: false,
		},
		// Unknown scheme is fail-closed.
		{
			name:    "ftp remote — reject (unknown scheme)",
			input:   "ftp://files.example.com/data",
			wantErr: true,
		},
		// unix:// with empty host is a local socket — ok.
		{
			name:    "unix socket — ok",
			input:   "unix:///var/run/redis.sock",
			wantErr: false,
		},
		// unix:// with a non-empty host must be rejected (F2).
		{
			name:    "unix with non-empty host — reject",
			input:   "unix://evil.host/x",
			wantErr: true,
		},
		// wss is TLS.
		{
			name:    "wss remote — ok",
			input:   "wss://ws.example.com/events",
			wantErr: false,
		},
		// Expanded loopback coverage (F2): 127.0.0.2 and IPv4-mapped IPv6.
		{
			name:    "bare 127.0.0.2:port — ok (loopback exception)",
			input:   "127.0.0.2:6379",
			wantErr: false,
		},
		{
			name:    "IPv4-mapped IPv6 loopback — ok",
			input:   "[::ffff:127.0.0.1]:8200",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := secutil.ValidateTLSEndpoint(tc.input)
			assertTLSEndpointResult(t, tc.input, err, tc.wantErr)
		})
	}
}

// assertTLSEndpointResult checks that err matches expectations for input. When
// wantErr is true the error must be a *errcode.Error tagged ErrAdapterEndpointNotTLS;
// otherwise err must be nil. Extracted to keep TestValidateTLSEndpoint's loop
// body within the cognitive-complexity budget.
func assertTLSEndpointResult(t *testing.T, input string, err error, wantErr bool) {
	t.Helper()
	if !wantErr {
		if err != nil {
			t.Errorf("ValidateTLSEndpoint(%q): want nil, got %v", input, err)
		}
		return
	}
	if err == nil {
		t.Errorf("ValidateTLSEndpoint(%q): want error, got nil", input)
		return
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != errcode.ErrAdapterEndpointNotTLS {
		t.Errorf("ValidateTLSEndpoint(%q): error %q does not have code ErrAdapterEndpointNotTLS",
			input, err.Error())
	}
}
