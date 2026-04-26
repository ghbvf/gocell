package secutil_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/secutil"
)

// TestValidateTLSEndpoint verifies the TLS-endpoint validation helper across
// the canonical cases defined by the SEC-FAIL-CLOSED loopback exception rule:
//   - Remote endpoints require a TLS scheme (https, rediss, tls-aware bare host is rejected).
//   - Loopback hosts (127.0.0.1, ::1, localhost) are exempt regardless of scheme.
//   - Empty endpoint is always rejected.
//
// TODO(phase2): replace string-assertion on "TLS" with errors.Is(err, errcode.ErrAdapterEndpointNotTLS)
// once the sentinel is added to pkg/errcode in phase 2.
func TestValidateTLSEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
		// errHint is checked via strings.Contains on err.Error() when wantErr=true.
		// TODO(phase2): replace with errors.Is(err, errcode.ErrAdapterEndpointNotTLS)
		errHint string
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
			errHint: "TLS",
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
			errHint: "TLS",
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
			errHint: "TLS",
		},
		{
			name:    "empty string — reject",
			input:   "",
			wantErr: true,
			errHint: "TLS",
		},
		// Additional coverage: IPv6 bracket form with port in URL host.
		{
			name:    "URL with [::1]:port loopback host — ok",
			input:   "redis://[::1]:6379",
			wantErr: false,
		},
		// Additional coverage: unknown scheme is fail-closed.
		{
			name:    "ftp remote — reject (unknown scheme)",
			input:   "ftp://files.example.com/data",
			wantErr: true,
			errHint: "TLS",
		},
		// Additional coverage: unix socket is TLS-equivalent (no network).
		{
			name:    "unix socket — ok",
			input:   "unix:///var/run/redis.sock",
			wantErr: false,
		},
		// Additional coverage: wss is TLS.
		{
			name:    "wss remote — ok",
			input:   "wss://ws.example.com/events",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := secutil.ValidateTLSEndpoint(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ValidateTLSEndpoint(%q): want error, got nil", tc.input)
					return
				}
				// TODO(phase2): switch to errors.Is(err, errcode.ErrAdapterEndpointNotTLS)
				if tc.errHint != "" && !strings.Contains(err.Error(), tc.errHint) {
					t.Errorf("ValidateTLSEndpoint(%q): error %q does not contain hint %q",
						tc.input, err.Error(), tc.errHint)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateTLSEndpoint(%q): want nil, got %v", tc.input, err)
				}
			}
		})
	}
}
