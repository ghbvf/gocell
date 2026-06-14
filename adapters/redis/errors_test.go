package redis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// fakeNetError is a synthetic net.Error used to drive both the timeout and
// non-timeout branches of the redis classifier.
type fakeNetError struct {
	timeout bool
}

func (e *fakeNetError) Error() string {
	if e.timeout {
		return "i/o timeout"
	}
	return "network error"
}

func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return false }

var _ net.Error = (*fakeNetError)(nil)

func TestClassifyRedisError(t *testing.T) {
	t.Parallel()

	const opCode = errcode.Code("ERR_ADAPTER_REDIS_GET")
	const opMsg = "get operation context"

	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{
			name:      "net.Error Timeout true → transient",
			err:       &fakeNetError{timeout: true},
			transient: true,
		},
		{
			// Post-fix: any net.Error in chain → transient (symmetric with
			// adapters/vault and adapters/s3, ADR 202605161800 §Adapter
			// transient inventory). Non-timeout transport errors represent
			// recoverable blips and must retry rather than DLX.
			name:      "net.Error Timeout false → transient",
			err:       &fakeNetError{timeout: false},
			transient: true,
		},
		{
			// Regression: dial-refused on a Redis container that is restarting
			// surfaces as *net.OpError + syscall.ECONNREFUSED. Post-fix this
			// is transient (S3-CLASSIFYERROR-CONN-REFUSED-01 funnel
			// generalization).
			name: "*net.OpError + ECONNREFUSED (dial refused) → transient",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: syscall.ECONNREFUSED,
			},
			transient: true,
		},
		{
			name:      "context.DeadlineExceeded → transient",
			err:       context.DeadlineExceeded,
			transient: true,
		},
		{
			name:      "context.Canceled → permanent",
			err:       context.Canceled,
			transient: false,
		},
		{
			name:      "LOADING prefix → transient",
			err:       errors.New("LOADING Redis is loading the dataset in memory"),
			transient: true,
		},
		{
			name:      "CLUSTERDOWN prefix → transient",
			err:       errors.New("CLUSTERDOWN The cluster is down"),
			transient: true,
		},
		{
			name:      "TRYAGAIN prefix → transient",
			err:       errors.New("TRYAGAIN Command cannot be processed, please try again"),
			transient: true,
		},
		{
			name:      "MASTERDOWN prefix → transient",
			err:       errors.New("MASTERDOWN Link with MASTER is down"),
			transient: true,
		},
		{
			name:      "WRONGTYPE prefix → permanent",
			err:       errors.New("WRONGTYPE Operation against a key holding the wrong kind of value"),
			transient: false,
		},
		{
			name:      "json marshal plain error → permanent",
			err:       errors.New("json: cannot unmarshal string into Go value of type int"),
			transient: false,
		},
		{
			name:      "i/o timeout string → transient",
			err:       errors.New("read tcp 127.0.0.1:0->127.0.0.1:6379: i/o timeout"),
			transient: true,
		},
		{
			name:      "goredis.ErrPoolTimeout → transient",
			err:       goredis.ErrPoolTimeout,
			transient: true,
		},
		{
			name:      "wrapped goredis.ErrPoolTimeout → transient",
			err:       fmt.Errorf("cache get: %w", goredis.ErrPoolTimeout),
			transient: true,
		},
		{
			// Semantic lock: when an error chain contains BOTH context.Canceled
			// and a net.Error (*net.OpError), errcode.IsTransientNet hits the
			// net.Error in the chain → transient. This documents the expected
			// branch-order behavior: net.Error check (IsTransientNet) fires
			// before the context.Canceled permanent check. Preventing future
			// accidental reordering that would route this to permanent.
			name: "context.Canceled wrapping *net.OpError — IsTransientNet hits, transient",
			err: fmt.Errorf("cancel: %w (net: %w)", context.Canceled,
				&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			transient: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyRedisError(tc.err, opCode, opMsg)
			if got == nil {
				t.Fatal("classifyRedisError must not return nil")
			}
			if errcode.IsTransient(got) != tc.transient {
				t.Errorf("IsTransient(%q) = %v, want %v (err: %v)",
					tc.err, errcode.IsTransient(got), tc.transient, got)
			}
		})
	}
}
