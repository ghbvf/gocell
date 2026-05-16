package redis

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// fakeNetError is a synthetic net.Error used to exercise the Timeout() branch.
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
			name:      "net.Error Timeout false → permanent",
			err:       &fakeNetError{timeout: false},
			transient: false,
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
