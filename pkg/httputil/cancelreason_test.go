package httputil

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxcancel"
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
// Only the canonical enum values (ctxcancel.ReasonCanceled / ReasonDeadlineExceeded)
// are accepted by setCancelReason — see TestCancelReason_RejectsUnknown for
// the fail-closed branch.
func TestCancelReason_RoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		reason string
	}{
		{name: "canceled", reason: ctxcancel.ReasonCanceled},
		{name: "deadline_exceeded", reason: ctxcancel.ReasonDeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCancelReasonSlot(context.Background())
			setCancelReason(ctx, tt.reason)
			assert.Equal(t, tt.reason, CancelReason(ctx))
		})
	}
}

// TestCancelReason_RejectsUnknown locks the fail-closed enum guard at the
// slot writer (PR275 P2-1 defence-in-depth): any value outside
// {ReasonCanceled, ReasonDeadlineExceeded} is silently dropped so neither
// log4xx cancel_reason fields nor span client.cancel.reason attributes can
// pollute their cardinality with arbitrary strings (user input
// misrouted via Details["reason"], future enum extensions that haven't
// migrated, or malicious upstream).
func TestCancelReason_RejectsUnknown(t *testing.T) {
	tests := []struct {
		name   string
		reason string
	}{
		{name: "empty string is dropped", reason: ""},
		{name: "future-enum value rejected", reason: "future-enum-value"},
		{name: "user-derived string rejected", reason: "key=admin@company.com"},
		{name: "html injection attempt rejected", reason: "<script>alert(1)</script>"},
		{name: "case-mismatch rejected", reason: "Canceled"}, // exact-match enum, not case-insensitive
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := WithCancelReasonSlot(context.Background())
			setCancelReason(ctx, tt.reason)
			assert.Equal(t, "", CancelReason(ctx),
				"non-enum reason values must be dropped, leaving the slot empty")
		})
	}
}

// TestCancelReason_RejectsUnknownPreservesPrior verifies the unknown-value
// drop does not erase a previously-set valid value. This is the same
// pattern as the prior empty-string guard but extended to all non-canonical
// values: a later malformed write must not corrupt an earlier good record.
func TestCancelReason_RejectsUnknownPreservesPrior(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())
	setCancelReason(ctx, ctxcancel.ReasonCanceled)
	setCancelReason(ctx, "garbage")          // must NOT clobber
	setCancelReason(ctx, "")                 // must NOT clobber
	setCancelReason(ctx, "deadline_unknown") // must NOT clobber

	assert.Equal(t, ctxcancel.ReasonCanceled, CancelReason(ctx),
		"unknown-value writes must be no-ops, preserving the earlier valid reason")
}

// TestCancelReason_LastValidWriteWins documents the last-valid-write-wins
// semantics for fan-out scenarios (rare today; cheap insurance). If a future
// middleware also writes the slot with a valid value, the most-recent reason
// becomes visible to tracing — which is the correct fan-in behaviour for
// "what reason did the response writer ultimately commit to".
func TestCancelReason_LastValidWriteWins(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())
	setCancelReason(ctx, ctxcancel.ReasonCanceled)
	setCancelReason(ctx, ctxcancel.ReasonDeadlineExceeded)

	assert.Equal(t, ctxcancel.ReasonDeadlineExceeded, CancelReason(ctx),
		"second valid write must overwrite the first")
}

// TestCancelReason_ConcurrentWrite exercises the sync.Mutex guard on the
// slot. Race detector (go test -race) catches torn writes here if the
// mutex regresses; without -race the test still verifies one of the values
// won. Since the slot is request-scoped, fan-out across goroutines is rare,
// but the cost of a single Mutex is negligible vs the safety guarantee.
func TestCancelReason_ConcurrentWrite(t *testing.T) {
	ctx := WithCancelReasonSlot(context.Background())

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(2)
		go func() { defer wg.Done(); setCancelReason(ctx, ctxcancel.ReasonCanceled) }()
		go func() { defer wg.Done(); setCancelReason(ctx, ctxcancel.ReasonDeadlineExceeded) }()
	}
	wg.Wait()

	got := CancelReason(ctx)
	assert.Contains(t,
		[]string{ctxcancel.ReasonCanceled, ctxcancel.ReasonDeadlineExceeded},
		got,
		"concurrent writes must yield one of the legal values, never a torn read")
}
