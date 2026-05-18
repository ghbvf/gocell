package errcode

import (
	"errors"
	"fmt"
	"net"
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
