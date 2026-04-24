package health

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapCtxSafe_ReturnsOnCtxDone_InnerIgnoresCtx is the contract test for
// PR-A35's structural wrapper: even when the inner function completely
// ignores ctx, the wrapped Checker must return ctx.Err() as soon as ctx is
// cancelled. The inner goroutine is expected to continue running; the test
// unblocks it on cleanup.
func TestWrapCtxSafe_ReturnsOnCtxDone_InnerIgnoresCtx(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })

	var innerStarted atomic.Bool
	wrapped := wrapCtxSafe(func(_ context.Context) error {
		innerStarted.Store(true)
		<-unblock
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	type result struct {
		err     error
		elapsed time.Duration
	}
	resCh := make(chan result, 1)
	go func() {
		close(started)
		start := time.Now()
		err := wrapped(ctx)
		resCh <- result{err: err, elapsed: time.Since(start)}
	}()
	<-started

	// Give the inner goroutine a chance to register before cancel fires,
	// so we're testing the cancel path rather than a start-race.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case r := <-resCh:
		assert.ErrorIs(t, r.err, context.Canceled,
			"wrapped Checker must return ctx.Err on cancel; got %v", r.err)
		assert.Less(t, r.elapsed, 100*time.Millisecond,
			"wrapped Checker must return promptly after cancel; got %v", r.elapsed)
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("wrapped Checker did not return within 500ms of ctx cancel")
	}
	assert.True(t, innerStarted.Load(), "inner fn should have started")
}

// TestWrapCtxSafe_PropagatesError_WhenInnerReturnsFirst covers the happy
// path: if inner fn returns before ctx is cancelled, its error propagates
// unchanged.
func TestWrapCtxSafe_PropagatesError_WhenInnerReturnsFirst(t *testing.T) {
	tests := []struct {
		name    string
		innerFn func(ctx context.Context) error
		wantErr string
	}{
		{"healthy", func(_ context.Context) error { return nil }, ""},
		{"domain error", func(_ context.Context) error { return fmt.Errorf("disk full") }, "disk full"},
		{
			"respects ctx",
			func(ctx context.Context) error {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Millisecond):
					return nil
				}
			},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := wrapCtxSafe(tt.innerFn)
			err := wrapped(context.Background())
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

// TestWrapCtxSafe_PanicBubbles verifies a panic inside inner fn bubbles out
// to the wrapped call site so that the outer recover fence in runOneProbe
// can catch it just as it would for an unwrapped Checker.
func TestWrapCtxSafe_PanicBubbles(t *testing.T) {
	wrapped := wrapCtxSafe(func(_ context.Context) error {
		panic("boom")
	})
	assert.PanicsWithValue(t, "boom", func() {
		_ = wrapped(context.Background())
	})
}

// TestWrapCtxSafe_NilInput ensures defensive wrapping of nil returns a
// Checker that fails closed rather than causing a nil-deref at call time.
func TestWrapCtxSafe_NilInput(t *testing.T) {
	wrapped := wrapCtxSafe(nil)
	require.NotNil(t, wrapped)
	err := wrapped(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil checker")
}

// TestCheckCtxRespected_PassesOnCooperativeProbe is a minimal smoke test for
// the exported CheckCtxRespected helper that probe authors will use in
// their own unit tests. A cooperative probe must cause zero failures.
func TestCheckCtxRespected_PassesOnCooperativeProbe(t *testing.T) {
	cooperative := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	// Use a sub-test so any t.Errorf inside CheckCtxRespected is visible
	// but doesn't fail the parent test for a probe that actually cooperates.
	CheckCtxRespected(t, cooperative, 50*time.Millisecond)
}

// TestCheckCtxRespected_DetectsUncooperativeProbe exercises the failure
// path by running the helper against a deliberately-stuck probe, capturing
// the t.Errorf call via a testing.TB spy.
func TestCheckCtxRespected_DetectsUncooperativeProbe(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	stuck := func(_ context.Context) error {
		<-unblock
		return nil
	}
	spy := &tbSpy{TB: t}
	CheckCtxRespected(spy, stuck, 30*time.Millisecond)
	assert.True(t, spy.errored, "CheckCtxRespected must flag an uncooperative probe")
	assert.Contains(t, spy.lastMsg, "did not return within")
}

// tbSpy captures t.Errorf calls without failing the enclosing test.
type tbSpy struct {
	testing.TB
	errored bool
	lastMsg string
}

func (s *tbSpy) Errorf(format string, args ...any) {
	s.errored = true
	s.lastMsg = fmt.Sprintf(format, args...)
}
