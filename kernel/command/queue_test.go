package command_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/stretchr/testify/assert"
)

// Compile-time check that InMemQueue satisfies the Queue interface.
var _ command.Queue = (*commandtest.InMemQueue)(nil)

func TestAckReason_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		r    command.AckReason
		want bool
	}{
		{command.AckReason(0), false},
		{command.AckSuccess, true},
		{command.AckFailed, true},
		{command.AckTimeout, true},
		{command.AckRejected, true},
		{command.AckReason(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.r.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.r.Valid())
		})
	}
}

func TestAckReason_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		r    command.AckReason
		want string
	}{
		{command.AckReason(0), "invalid"},
		{command.AckSuccess, "success"},
		{command.AckFailed, "failed"},
		{command.AckTimeout, "timeout"},
		{command.AckRejected, "rejected"},
		{command.AckReason(99), "ack_reason(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.r.String())
		})
	}
}

func TestEnqueueOptions_ZeroValueIsValid(t *testing.T) {
	t.Parallel()
	// Zero-value EnqueueOptions should be usable without panics.
	// LeaseDuration is intentionally absent from EnqueueOptions — lease is
	// determined by Queue.Dequeue's leaseDuration parameter.
	opts := command.EnqueueOptions{}
	assert.Empty(t, opts.IdempotencyKey)
	assert.Nil(t, opts.Authz)
}
