package errcode

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"testing"
)

// fakeNetError is a synthetic net.Error implementation used to drive both
// timeout and non-timeout branches of IsTransientNet without standing up a
// real socket.
type fakeNetError struct{ timeout bool }

func (e *fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return false }

var _ net.Error = (*fakeNetError)(nil)

func TestIsTransientNet(t *testing.T) {
	t.Parallel()

	connRefused := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: syscall.ECONNREFUSED,
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain errors.New", err: errors.New("boom"), want: false},
		{
			name: "net.Error Timeout=true",
			err:  &fakeNetError{timeout: true},
			want: true,
		},
		{
			name: "net.Error Timeout=false (non-timeout net error)",
			err:  &fakeNetError{timeout: false},
			want: true,
		},
		{
			name: "*net.OpError + ECONNREFUSED (dial refused)",
			err:  connRefused,
			want: true,
		},
		{
			name: "*net.OpError wrapped via fmt.Errorf",
			err:  fmt.Errorf("adapter: %w", connRefused),
			want: true,
		},
		{
			name: "*net.DNSError",
			err:  &net.DNSError{Err: "no such host", Name: "example.invalid"},
			want: true,
		},
		{
			// errors.Join multi-chain: net.Error anywhere in the joined chain
			// must be detected (errors.As traverses joined errors).
			name: "errors.Join with *net.OpError in second branch → transient",
			err: errors.Join(
				errors.New("other error"),
				&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			),
			want: true,
		},
		{
			// NXDOMAIN: *net.DNSError with IsNotFound=true is still a net.Error;
			// accepted trade-off per ADR 202605161800 — any net.Error → transient
			// (DNS misconfiguration is transient in the sense that a retry may
			// reach a repaired resolver or re-resolve successfully).
			name: "*net.DNSError NXDOMAIN (IsNotFound=true) → transient (accepted trade-off per ADR 202605161800)",
			err:  &net.DNSError{Err: "no such host", Name: "example.invalid", IsNotFound: true},
			want: true,
		},
		{
			// *url.Error Op="parse" — operator-side URL misconfiguration.
			// Permanent: retry will never fix a malformed endpoint string.
			// ref: aws-sdk-go-v2 retry/retryable_error.go url.Error handling.
			name: "*url.Error Op=parse (URL misconfig) → permanent",
			err: &url.Error{
				Op:  "parse",
				URL: "://invalid",
				Err: errors.New("missing scheme"),
			},
			want: false,
		},
		{
			// *url.Error Op="Get" wrapping a real *net.OpError dial-refused —
			// HTTP client wraps transport-layer errors in *url.Error. The inner
			// cause is transient; helper must recurse on urlErr.Err.
			name: "*url.Error Op=Get wrapping *net.OpError (dial refused) → transient",
			err: &url.Error{
				Op:  "Get",
				URL: "http://example.invalid/path",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			},
			want: true,
		},
		{
			// *url.Error Op="Get" wrapping a non-net.Error cause (e.g. content
			// decode failure). Recursion finds no net.Error → permanent.
			name: "*url.Error Op=Get wrapping plain error → permanent",
			err: &url.Error{
				Op:  "Get",
				URL: "http://example.com/path",
				Err: errors.New("content decode failed"),
			},
			want: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := IsTransientNet(tc.err)
			if got != tc.want {
				t.Fatalf("IsTransientNet(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
