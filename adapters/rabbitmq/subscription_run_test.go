package rabbitmq

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingChannel wraps mockChannel and records ordering information for A19
// happens-before assertions.
//
// Two complementary ordering proofs are supported:
//
//  1. Wall-clock: closeTime records the timestamp of Close() for tests that
//     compare against Ack timestamps (subscriber_close_ctx_test.go A19 E2E).
//
//  2. Causal flag: deliveryDoneFlag is set atomically by the test immediately
//     before markDeliveryDone; closeSeenDoneFlag records whether the flag was
//     already set when Close() ran. This proves wg.Done happened-before ch.Close
//     without relying on clock resolution (TestSubscriptionRun_CloseWaitsLocalWg).
type recordingChannel struct {
	*mockChannel
	// closeTime records when Close() was called — used by A19 E2E timestamp tests.
	closeTime atomic.Pointer[time.Time]
	// deliveryDoneFlag is set by the test before markDeliveryDone. Close() checks
	// that it is already set, establishing the happens-before: wg.Done → ch.Close.
	deliveryDoneFlag atomic.Bool
	// closeSeenDoneFlag records whether deliveryDoneFlag was observed true inside
	// Close(), verifying the A19 causal ordering guarantee at the call site.
	closeSeenDoneFlag atomic.Bool
	closeCount        atomic.Int32
}

func newRecordingChannel() *recordingChannel {
	return &recordingChannel{mockChannel: newMockChannel()}
}

func (r *recordingChannel) Close() error {
	r.closeCount.Add(1)
	t := time.Now()
	r.closeTime.Store(&t)
	// Record whether the delivery-done flag was set BEFORE Close was called.
	// If the A19 invariant holds (wg.Wait before ch.Close), this must always be true.
	r.closeSeenDoneFlag.Store(r.deliveryDoneFlag.Load())
	return r.mockChannel.Close()
}

// TestSubscriptionRun_New verifies that newSubscriptionRun populates all fields.
func TestSubscriptionRun_New(t *testing.T) {
	ch := newMockChannel()
	const tag = "cg-test-queue-test.topic"

	run := newSubscriptionRun(ch, tag)

	require.NotNil(t, run, "newSubscriptionRun must return non-nil")
	assert.Equal(t, ch, run.ch, "ch must match the argument")
	assert.Equal(t, tag, run.consumerTag, "consumerTag must match the argument")
}

// TestSubscriptionRun_RegisterDeliveryAndWait verifies that N registerDelivery calls
// are tracked by localWg and that waitAndClose (with ample ctx) returns only after all
// markDeliveryDone calls.
func TestSubscriptionRun_RegisterDeliveryAndWait(t *testing.T) {
	ch := newMockChannel()
	run := newSubscriptionRun(ch, "cg-test-wait")

	const n = 5
	for range n {
		run.registerDelivery()
	}

	// Release all in-flight goroutines after a short delay so waitAndClose can return.
	go func() {
		time.Sleep(50 * time.Millisecond)
		for range n {
			run.markDeliveryDone()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := run.waitAndClose(ctx)
	elapsed := time.Since(start)

	require.NoError(t, err, "waitAndClose must return nil when all deliveries complete")
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond,
		"waitAndClose must have actually waited for deliveries to finish")
	assert.True(t, ch.closeCalled, "ch.Close must be called after wg.Wait")
}

// TestSubscriptionRun_CloseWaitsLocalWg asserts the A19 ordering guarantee:
// ch.Close must NOT be called before localWg.Wait() returns.
//
// We verify causal ordering via an atomic flag rather than wall-clock timestamps:
// rc.deliveryDoneFlag is set atomically immediately before markDeliveryDone is called.
// recordingChannel.Close() reads that flag inside the call and stores the observation
// in closeSeenDoneFlag. If the A19 invariant holds (wg.Wait before ch.Close), the flag
// must always be observed as true — regardless of clock resolution.
func TestSubscriptionRun_CloseWaitsLocalWg(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-a19-order")

	run.registerDelivery()

	done := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		// Set the flag atomically BEFORE calling markDeliveryDone. Because
		// wgDoneCh is closed (and ch.Close called) only after localWg.Wait()
		// returns, Close() can only execute after this store — establishing the
		// happens-before relationship we are asserting.
		rc.deliveryDoneFlag.Store(true)
		run.markDeliveryDone()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := run.waitAndClose(ctx)
	<-done

	require.NoError(t, err)
	assert.Equal(t, int32(1), rc.closeCount.Load(), "ch.Close must have been called exactly once")
	assert.True(t, rc.closeSeenDoneFlag.Load(),
		"ch.Close must observe deliveryDoneFlag=true, proving wg.Wait happened-before ch.Close (A19)")
}

// TestSubscriptionRun_WaitAndClose_Idempotent verifies that calling waitAndClose
// a second time does not call ch.Close again (sync.Once guard).
func TestSubscriptionRun_WaitAndClose_Idempotent(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-idempotent")

	ctx := context.Background()
	require.NoError(t, run.waitAndClose(ctx))
	require.NoError(t, run.waitAndClose(ctx))

	assert.Equal(t, int32(1), rc.closeCount.Load(),
		"ch.Close must be called exactly once across multiple waitAndClose calls")
}

// TestSubscriptionRun_CtxTimeout verifies that waitAndClose returns ctx.Err
// when the context deadline expires before all in-flight deliveries complete.
func TestSubscriptionRun_CtxTimeout(t *testing.T) {
	ch := newMockChannel()
	run := newSubscriptionRun(ch, "cg-ctx-timeout")

	// Add a delivery that will never complete within the ctx budget.
	run.registerDelivery()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := run.waitAndClose(ctx)
	elapsed := time.Since(start)

	require.Error(t, err, "waitAndClose must return an error when ctx expires")
	assert.True(t, errors.Is(err, context.DeadlineExceeded),
		"error must be context.DeadlineExceeded, got: %v", err)
	assert.Less(t, elapsed, 500*time.Millisecond,
		"waitAndClose must return promptly after ctx expiry")

	// The inflight delivery is never completed — release it to avoid goroutine leak.
	t.Cleanup(func() { run.markDeliveryDone() })
}

// TestSubscriptionRun_WaitAndClose_CtxTimeout_AbandonedGoroutineEventuallyExits
// verifies that when waitAndClose returns DeadlineExceeded (ctx expired before
// in-flight deliveries drained), the internal wg-waiter goroutine does NOT
// leak permanently. After releasing the delivery via markDeliveryDone(), the
// abandoned goroutine must exit.
//
// Goroutine-exit is verified via the happens-before channel signal from
// run.wgDone(): after markDeliveryDone unblocks localWg.Wait(), the wg-waiter
// goroutine closes wgDoneCh, which this test receives within a 1 s window.
// Using a channel signal instead of runtime.NumGoroutine() avoids false
// negatives from GC goroutines and other test-framework noise.
func TestSubscriptionRun_WaitAndClose_CtxTimeout_AbandonedGoroutineEventuallyExits(t *testing.T) {
	ch := newMockChannel()
	run := newSubscriptionRun(ch, "cg-abandon-exits")

	// Register one in-flight delivery; the goroutine inside waitAndClose will
	// block on localWg.Wait() until we call markDeliveryDone below.
	run.registerDelivery()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	err := run.waitAndClose(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"waitAndClose must return DeadlineExceeded when ctx expires with in-flight delivery")

	// Channel must NOT have been closed: Phase 2 is skipped on ctx expiry.
	assert.False(t, ch.closeCalled,
		"ch.Close must not be called when waitAndClose returns early due to ctx expiry")

	// Capture wgDone channel; it is initialised by newSubscriptionRun and
	// will be closed when the wg-waiter goroutine (spawned inside waitAndClose)
	// finishes localWg.Wait().
	wgDone := run.wgDone()

	// Release the in-flight delivery — this unblocks the abandoned wg-waiter goroutine.
	run.markDeliveryDone()

	// The wg-waiter goroutine must exit: wgDoneCh is closed as a happens-before
	// signal immediately after localWg.Wait() returns.  1 s is generous enough
	// to tolerate slow CI machines while still catching real leaks.
	select {
	case <-wgDone:
		// goroutine exited as expected
	case <-time.After(1 * time.Second):
		t.Fatal("wg-waiter goroutine did not exit within 1 s after markDeliveryDone")
	}
}
