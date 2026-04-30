package health

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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

	innerReady := make(chan struct{})
	wrapped := wrapCtxSafe(func(_ context.Context) error {
		close(innerReady) // happens-before signal: inner fn is parked
		<-unblock
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		err     error
		elapsed time.Duration
	}
	resCh := make(chan result, 1)
	go func() {
		start := time.Now()
		err := wrapped(ctx)
		resCh <- result{err: err, elapsed: time.Since(start)}
	}()

	// Wait for a happens-before signal instead of sleeping: innerReady is
	// closed by the inner fn before it blocks on unblock, so we are
	// guaranteed the cancel path (not a start-race) is exercised.
	<-innerReady
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

// TestWrapCtxSafe_PanicBecomesErrorAndLogs verifies a panic inside inner fn is
// converted into an unhealthy probe error and logged instead of re-panicking
// through the wrapper.
func TestWrapCtxSafe_PanicBecomesErrorAndLogs(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	wrapped := wrapCtxSafe(func(_ context.Context) error {
		panic("boom")
	})
	err := wrapped(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic: boom")
	assert.Contains(t, logs.String(), "health: probe panicked")
	assert.Contains(t, logs.String(), "boom")
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

// CheckCtxRespected lives in runtime/http/healthtest so the testing import
// does not leak into the production health package. See
// runtime/http/healthtest/healthtest_test.go for the equivalent
// cooperative/uncooperative coverage.
