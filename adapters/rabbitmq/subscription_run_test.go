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
