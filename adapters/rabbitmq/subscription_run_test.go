package rabbitmq

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingChannel wraps mockChannel and records Close call timestamps so
// A19 ordering assertions can compare ackCallOrder vs closeCallOrder.
type recordingChannel struct {
	*mockChannel
	closeTime  atomic.Pointer[time.Time]
	closeCount atomic.Int32
}

func newRecordingChannel() *recordingChannel {
	return &recordingChannel{mockChannel: newMockChannel()}
}

func (r *recordingChannel) Close() error {
	r.closeCount.Add(1)
	t := time.Now()
	r.closeTime.Store(&t)
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
// We verify this by checking that the channel's Close timestamp is after
// the last markDeliveryDone call.
func TestSubscriptionRun_CloseWaitsLocalWg(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-a19-order")

	run.registerDelivery()

	var lastDoneTime time.Time
	done := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		lastDoneTime = time.Now()
		run.markDeliveryDone()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := run.waitAndClose(ctx)
	<-done

	require.NoError(t, err)
	require.NotNil(t, rc.closeTime.Load(), "ch.Close must have been called")
	closeT := *rc.closeTime.Load()
	assert.True(t, closeT.After(lastDoneTime) || closeT.Equal(lastDoneTime),
		"ch.Close (%s) must happen after markDeliveryDone (%s)", closeT, lastDoneTime)
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
// abandoned goroutine must exit within a generous window so that long-running
// test suites do not accumulate goroutine leaks from successive reconnect cycles.
//
// Goroutine-exit is verified by: waiting for the count to rise (wg-waiter
// spawned), then checking it falls back to the per-call baseline after
// markDeliveryDone. The per-call baseline is taken just before waitAndClose so
// that the already-live test-framework goroutines are included and we only
// track the delta introduced by the wg-waiter.
func TestSubscriptionRun_WaitAndClose_CtxTimeout_AbandonedGoroutineEventuallyExits(t *testing.T) {
	ch := newMockChannel()
	run := newSubscriptionRun(ch, "cg-abandon-exits")

	// Register one in-flight delivery; the goroutine inside waitAndClose will
	// block on localWg.Wait() until we call markDeliveryDone below.
	run.registerDelivery()

	// Capture baseline immediately before calling waitAndClose so the
	// wg-waiter goroutine has not started yet.
	baselineCount := runtime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	err := run.waitAndClose(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"waitAndClose must return DeadlineExceeded when ctx expires with in-flight delivery")

	// Channel must NOT have been closed: Phase 2 is skipped on ctx expiry.
	assert.False(t, ch.closeCalled,
		"ch.Close must not be called when waitAndClose returns early due to ctx expiry")

	// Confirm the wg-waiter goroutine is alive: the count must rise above
	// baseline while the delivery is still in-flight.
	var countWhileBlocked int
	require.Eventually(t, func() bool {
		countWhileBlocked = runtime.NumGoroutine()
		return countWhileBlocked > baselineCount
	}, 200*time.Millisecond, 5*time.Millisecond,
		"wg-waiter goroutine must be observable after ctx-expired waitAndClose (baseline was %d)",
		baselineCount)

	// Release the in-flight delivery — this unblocks the abandoned wg-waiter goroutine.
	run.markDeliveryDone()

	// The abandoned goroutine must exit: count must drop below its peak.
	assert.Eventually(t, func() bool {
		return runtime.NumGoroutine() < countWhileBlocked
	}, 500*time.Millisecond, 10*time.Millisecond,
		"goroutine count must decrease from peak (%d) after markDeliveryDone unblocks the abandoned wg-waiter",
		countWhileBlocked)
}
