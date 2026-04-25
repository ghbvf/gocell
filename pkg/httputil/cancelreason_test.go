package httputil

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCancelReason_NoSlot verifies the no-op contract when no slot was
// installed — CancelReason returns "" and setCancelReason silently drops
// the write. This is the path raw-499 unit tests / non-tracing handlers
// rely on; a panic here would crash any handler that calls
// httputil.WriteDomainError outside of the tracing middleware chain.
func TestCancelReason_NoSlot(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, "", CancelReason(ctx),
		"empty ctx must yield empty CancelReason, not panic")

	// setCancelReason is a no-op when no slot is present; should not panic.
	setCancelReason(ctx, "canceled")
	assert.Equal(t, "", CancelReason(ctx),
		"writing into no-slot ctx must remain a silent no-op")
}

// TestCancelReason_RoundTrip locks the basic install → set → read pipeline
// for the slot. tracing middleware exercises the same flow at request scope.
func TestCancelReason_RoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		reason string
	}{
		{name: "canceled", reason: "canceled"},
		{name: "deadline_exceeded", reason: "deadline_exceeded"},
		{name: "non-empty arbitrary", reason: "future-enum-value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCancelReasonSlot(context.Background())
			setCancelReason(ctx, tt.reason)
			assert.Equal(t, tt.reason, CancelReason(ctx))
		})
	}
}

// TestCancelReason_EmptySetSkipped guards the explicit empty-string skip
// in setCancelReason: callers reading ecErr.Details["reason"] may get an
// empty string when the helper has not populated it yet, and silently
// skipping the write avoids overwriting a real reason set earlier in the
// request lifecycle.
func TestCancelReason_EmptySetSkipped(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())
	setCancelReason(ctx, "canceled")
	setCancelReason(ctx, "") // must NOT clobber the prior reason

	assert.Equal(t, "canceled", CancelReason(ctx),
		"empty-string set must be a no-op, preserving the earlier reason")
}

// TestCancelReason_LastWriteWins documents the explicit last-write-wins
// semantics for fan-out scenarios (rare today; cheap insurance). If a future
// middleware also writes the slot, the most-recent reason becomes visible
// to tracing — which is the correct fan-in behaviour for "what reason did
// the response writer ultimately commit to".
func TestCancelReason_LastWriteWins(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())
	setCancelReason(ctx, "canceled")
	setCancelReason(ctx, "deadline_exceeded")

	assert.Equal(t, "deadline_exceeded", CancelReason(ctx),
		"second non-empty write must overwrite the first")
}

// TestCancelReason_ConcurrentWrite exercises the sync.Mutex guard on the
// slot. Race detector (go test -race) catches torn writes here if the
// mutex regresses; without -race the test still verifies one of the values
// won. Since the slot is request-scoped, fan-out across goroutines is rare,
// but the cost of a single Mutex is negligible vs the safety guarantee.
func TestCancelReason_ConcurrentWrite(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); setCancelReason(ctx, "canceled") }()
		go func() { defer wg.Done(); setCancelReason(ctx, "deadline_exceeded") }()
	}
	wg.Wait()

	got := CancelReason(ctx)
	assert.Contains(t, []string{"canceled", "deadline_exceeded"}, got,
		"concurrent writes must yield one of the legal values, never a torn read")
}
