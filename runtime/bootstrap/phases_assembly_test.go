package bootstrap

// phases_assembly_test.go — unit tests for config-reload ctx propagation and
// invokeReloader timeout behavior.
//
// ref: etcd clientv3 Watch ctx propagation.
// ref: k8s SharedInformer ctx propagation.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// reloadTestTimeout is the short deadline injected during testing.
// Named const per G6 TEST-TIME-LITERAL rule — no bare time.Duration literal.
const reloadTestTimeout = testtime.D10ms

// reloadTestCallbackSleep is the sleep duration that exceeds reloadTestTimeout,
// used to force a DeadlineExceeded in TestInvokeReloader_TimeoutCancelsCallback.
const reloadTestCallbackSleep = testtime.D100ms

// TestNotifyCellsConfigChanged_RespectsCallerCtx verifies that the context
// passed to notifyCellsConfigChanged (and on to invokeReloader) is propagated
// into the callback — so that canceling the reload ctx causes ctx.Err() !=
// nil inside the callback.
//
// ref: etcd clientv3 Watch — watchers propagate the caller's ctx.
func TestNotifyCellsConfigChanged_RespectsCallerCtx(t *testing.T) {
	t.Parallel()

	var callbackCtxErr error
	callbackDone := make(chan struct{})

	fn := func(ctx context.Context, _ cell.ConfigChangeEvent) error {
		defer close(callbackDone)
		// Block until ctx is canceled, then capture ctx.Err().
		<-ctx.Done()
		callbackCtxErr = ctx.Err()
		return callbackCtxErr
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so the callback receives an already-done ctx

	ok := invokeReloader(cancelCtx, "test-cell", fn, cell.ConfigChangeEvent{}, 0)

	select {
	case <-callbackDone:
	case <-time.After(testtime.CtxDefault):
		t.Fatal("callback did not complete within timeout")
	}

	assert.False(t, ok, "invokeReloader should return false when callback returns error")
	assert.NotNil(t, callbackCtxErr, "callback ctx.Err() must be non-nil after ctx cancellation")
	assert.True(t, errors.Is(callbackCtxErr, context.Canceled),
		"expected context.Canceled, got %v", callbackCtxErr)
}

// TestInvokeReloader_TimeoutCancelsCallback verifies that when the assembly
// ReloadTimeout fires before the callback returns, the callback receives a
// context with ctx.Err() == context.DeadlineExceeded and invokeReloader
// returns false.
//
// ref: k8s SharedInformer — informer callbacks carry a bounded ctx.
func TestInvokeReloader_TimeoutCancelsCallback(t *testing.T) {
	t.Parallel()

	var callbackCtxErr error
	callbackDone := make(chan struct{})

	fn := func(ctx context.Context, _ cell.ConfigChangeEvent) error {
		defer close(callbackDone)
		// Sleep longer than the per-call timeout so the deadline fires first.
		select {
		case <-ctx.Done():
			callbackCtxErr = ctx.Err()
			return callbackCtxErr
		case <-time.After(reloadTestCallbackSleep):
			// Should not reach here during a correct implementation.
			return nil
		}
	}

	start := time.Now()
	ok := invokeReloader(context.Background(), "test-cell", fn, cell.ConfigChangeEvent{}, reloadTestTimeout)
	elapsed := time.Since(start)

	select {
	case <-callbackDone:
	case <-time.After(testtime.CtxDefault):
		t.Fatal("callback did not complete within timeout")
	}

	assert.False(t, ok, "invokeReloader should return false when callback is canceled by timeout")
	assert.NotNil(t, callbackCtxErr, "callback ctx.Err() must be non-nil after deadline")
	assert.True(t, errors.Is(callbackCtxErr, context.DeadlineExceeded),
		"expected DeadlineExceeded, got %v", callbackCtxErr)
	// The function should return well before the callback's own sleep finishes.
	assert.Less(t, elapsed, reloadTestCallbackSleep,
		"invokeReloader should return promptly after timeout, not wait for callback sleep")
}
