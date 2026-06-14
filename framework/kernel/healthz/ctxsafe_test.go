package healthz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// ctxSafeTimeout is the deadline each sub-test uses when waiting for Check to
// return. Long enough that real goroutine scheduling jitter never fires it, but
// short enough to surface a deadlock within the test run budget.
const ctxSafeTimeout = testtime.EventuallyShort

// cancelLagOverWatchThreshold is a fake-clock advance large enough that
// `Since(cancelAt) > 1s` triggers watchLateOutcome's "slow probe" warn branch
// (see TEST-TIME-LITERAL-01 — extract test-time durations to a package const).
const cancelLagOverWatchThreshold = 2 * time.Second

// TestWrapCtxSafe_ConstructorAndNameDelegation verifies that WrapCtxSafe returns
// a Probe whose Name() transparently delegates to the inner Probe.
func TestWrapCtxSafe_ConstructorAndNameDelegation(t *testing.T) {
	t.Parallel()

	inner := NewProbe("db_ready", func(_ context.Context) error { return nil })
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	if got := wrapped.Name(); got != "db_ready" {
		t.Errorf("Name() = %q, want %q", got, "db_ready")
	}
}

// TestWrapCtxSafe_HealthyCheckReturnsNil verifies that when the inner Check
// returns nil the wrapper propagates nil to the caller.
func TestWrapCtxSafe_HealthyCheckReturnsNil(t *testing.T) {
	t.Parallel()

	inner := NewProbe("db_ready", func(_ context.Context) error { return nil })
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithTimeout(context.Background(), ctxSafeTimeout)
	defer cancel()

	if err := wrapped.Check(ctx); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
}

// TestWrapCtxSafe_InnerErrorReturnedTransparently verifies that when the inner
// Check returns a non-nil error the wrapper returns that same error.
func TestWrapCtxSafe_InnerErrorReturnedTransparently(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("db unavailable")
	inner := NewProbe("db_ready", func(_ context.Context) error { return wantErr })
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithTimeout(context.Background(), ctxSafeTimeout)
	defer cancel()

	got := wrapped.Check(ctx)
	if !errors.Is(got, wantErr) {
		t.Errorf("Check() = %v, want %v", got, wantErr)
	}
}

// TestWrapCtxSafe_CtxCanceledReturnsCtxErr verifies the primary ctx-racing
// guarantee: if ctx is already canceled when Check starts, the wrapper returns
// ctx.Err() without waiting for the inner goroutine to finish.
func TestWrapCtxSafe_CtxCanceledReturnsCtxErr(t *testing.T) {
	t.Parallel()

	// Inner probe blocks until its own ctx is done; it cooperates correctly but
	// represents any I/O-bound probe that respects cancellation.
	inner := NewProbe("slow_ready", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Check

	err := wrapped.Check(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Check() = %v, want context.Canceled", err)
	}
}

// TestWrapCtxSafe_CtxDeadlineExceededReturnsCtxErr verifies that a
// context.DeadlineExceeded is returned when the deadline fires before the
// inner Check completes.
func TestWrapCtxSafe_CtxDeadlineExceededReturnsCtxErr(t *testing.T) {
	t.Parallel()

	// Inner probe blocks until its ctx is done (simulates an unresponsive probe).
	inner := NewProbe("slow_ready", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	// Use a very short deadline so the test completes quickly.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D1ms)
	defer cancel()

	// Wait until the deadline fires.
	<-ctx.Done()

	err := wrapped.Check(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Check() = %v, want context.DeadlineExceeded", err)
	}
}

// TestWrapCtxSafe_InnerPanicCaughtAndRedacted verifies that a panic inside the
// inner Check is caught, its payload is redacted, and Check returns an error
// whose message contains the "panic:" prefix plus the redacted value.
func TestWrapCtxSafe_InnerPanicCaughtAndRedacted(t *testing.T) {
	t.Parallel()

	// Inner probe panics with a string that contains a sensitive key=value pair.
	inner := NewProbe("crash_ready", func(_ context.Context) error {
		panic("secret=supersecret")
	})
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithTimeout(context.Background(), ctxSafeTimeout)
	defer cancel()

	err := wrapped.Check(ctx)
	if err == nil {
		t.Fatal("Check() = nil, want non-nil error after inner panic")
	}
	msg := err.Error()
	// The error must start with "panic:".
	if !strings.HasPrefix(msg, "panic:") {
		t.Errorf("error message %q does not start with \"panic:\"", msg)
	}
	// The sensitive value must be redacted.
	if strings.Contains(msg, "supersecret") {
		t.Errorf("error message %q leaks sensitive value \"supersecret\"", msg)
	}
}

// TestWrapCtxSafe_InnerPanicWithNilPayload verifies that panic(nil) is handled
// safely and returns an error (not a nil error which could be misinterpreted).
func TestWrapCtxSafe_InnerPanicWithNilPayload(t *testing.T) {
	t.Parallel()

	inner := NewProbe("nil_panic_ready", func(_ context.Context) error {
		panic(nil)
	})
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithTimeout(context.Background(), ctxSafeTimeout)
	defer cancel()

	err := wrapped.Check(ctx)
	if err == nil {
		t.Fatal("Check() = nil, want non-nil error after inner panic(nil)")
	}
	if !strings.HasPrefix(err.Error(), "panic:") {
		t.Errorf("error message %q does not start with \"panic:\"", err.Error())
	}
}

// TestWrapCtxSafe_InnerPanicErrorPayload verifies that a panic with an error
// payload is caught and its message is preserved (possibly redacted).
func TestWrapCtxSafe_InnerPanicErrorPayload(t *testing.T) {
	t.Parallel()

	panicErr := errors.New("internal failure")
	inner := NewProbe("err_panic_ready", func(_ context.Context) error {
		panic(panicErr)
	})
	clk := clockmock.New(time.Time{})
	wrapped := WrapCtxSafe(inner, clk)

	ctx, cancel := context.WithTimeout(context.Background(), ctxSafeTimeout)
	defer cancel()

	err := wrapped.Check(ctx)
	if err == nil {
		t.Fatal("Check() = nil, want non-nil error after inner panic with error payload")
	}
	if !strings.HasPrefix(err.Error(), "panic:") {
		t.Errorf("error message %q does not start with \"panic:\"", err.Error())
	}
	if !strings.Contains(err.Error(), "internal failure") {
		t.Errorf("error message %q does not contain the inner error text", err.Error())
	}
}

// TestWatchLateOutcome_PanicAfterCancellation exercises the background watcher
// path when a panicking inner probe fires after ctx cancellation. The test
// verifies that watchLateOutcome does not block indefinitely and drains the
// channel exactly once.
func TestWatchLateOutcome_PanicAfterCancellation(t *testing.T) {
	t.Parallel()

	// Create a done channel and pre-load a panic outcome so watchLateOutcome
	// returns immediately without any real goroutine scheduling.
	done := make(chan probeOutcome, 1)
	done <- probeOutcome{panicV: "token=leaked"}

	clk := clockmock.New(time.Time{})
	start := clk.Now()
	cancelAt := clk.Now()

	// watchLateOutcome should drain the channel and return without blocking.
	watchDone := make(chan struct{})
	go func() {
		watchLateOutcome("db_ready", context.Canceled, start, cancelAt, done, clk)
		close(watchDone)
	}()

	select {
	case <-watchDone:
		// pass
	case <-time.After(ctxSafeTimeout):
		t.Error("watchLateOutcome blocked unexpectedly")
	}
}

// TestWatchLateOutcome_NormalReturnAfterCancellation exercises the non-panic
// branch of watchLateOutcome (cancelLag < 1s path).
func TestWatchLateOutcome_NormalReturnAfterCancellation(t *testing.T) {
	t.Parallel()

	done := make(chan probeOutcome, 1)
	done <- probeOutcome{err: nil} // inner succeeded cleanly after cancellation

	clk := clockmock.New(time.Time{})
	start := clk.Now()
	// cancelAt = start so cancelLag = 0, which is < 1s → debug log branch.
	cancelAt := clk.Now()

	watchDone := make(chan struct{})
	go func() {
		watchLateOutcome("db_ready", context.Canceled, start, cancelAt, done, clk)
		close(watchDone)
	}()

	select {
	case <-watchDone:
		// pass
	case <-time.After(ctxSafeTimeout):
		t.Error("watchLateOutcome blocked unexpectedly")
	}
}

// TestWatchLateOutcome_SlowProbeWarnPath exercises the cancelLag > 1s warn
// branch by pre-advancing the fake clock so Since(cancelAt) returns > 1s.
func TestWatchLateOutcome_SlowProbeWarnPath(t *testing.T) {
	t.Parallel()

	done := make(chan probeOutcome, 1)
	done <- probeOutcome{err: errors.New("inner error")}

	clk := clockmock.New(time.Time{})
	start := clk.Now()
	cancelAt := clk.Now()
	// Advance the clock so cancelLag = Since(cancelAt) > 1s.
	clk.Advance(cancelLagOverWatchThreshold)

	watchDone := make(chan struct{})
	go func() {
		watchLateOutcome("db_ready", context.Canceled, start, cancelAt, done, clk)
		close(watchDone)
	}()

	select {
	case <-watchDone:
		// pass
	case <-time.After(ctxSafeTimeout):
		t.Error("watchLateOutcome blocked unexpectedly on warn path")
	}
}
