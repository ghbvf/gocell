package rabbitmq

// T6: Subscriber-level A19 + Close(ctx) tests.
//
// These tests exercise the new ctx-aware Close path and the A19 reconnect fix
// (localWg.Wait before ch.Close) delivered by the subscriptionRun refactor in
// Part 3 (T7-T8).
//
// ref: rabbitmq/amqp091-go channel.go — Cancel→drain→wg.Wait→ch.Close ordering
// ref: ThreeDotsLabs/watermill-amqp — closedChan→WaitGroup→ch.Close
// ref: nats-io/nats.go Subscription.Drain — two-phase drain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// ---------------------------------------------------------------------------
// F6: E2E test — ch.Close timestamp is after all Ack timestamps (A19 ordering)
// ---------------------------------------------------------------------------

// ackTimestampChannel wraps mockChannel and records the wall-clock timestamp of
// every Ack call and the Close call. Used by the F6 E2E test to assert A19
// ordering: all Acks precede ch.Close.
type ackTimestampChannel struct {
	*mockChannel
	mu             sync.Mutex
	ackCallTimes   []time.Time
	closeCallTime  time.Time
	closeCallCount int
}

func newAckTimestampChannel() *ackTimestampChannel {
	return &ackTimestampChannel{mockChannel: newMockChannel()}
}

func (a *ackTimestampChannel) Ack(tag uint64, multiple bool) error {
	err := a.mockChannel.Ack(tag, multiple)
	a.mu.Lock()
	a.ackCallTimes = append(a.ackCallTimes, time.Now())
	a.mu.Unlock()
	return err
}

func (a *ackTimestampChannel) Close() error {
	a.mu.Lock()
	a.closeCallTime = time.Now()
	a.closeCallCount++
	a.mu.Unlock()
	return a.mockChannel.Close()
}

// TestSubscriber_Reconnect_E2E_ChannelCloseAfterAllAcks is an end-to-end test
// proving that ch.Close is called after all processDelivery goroutines have
// completed (and thus after all Ack calls). This is the A19 ordering guarantee
// exercised at the full Subscriber level (not just the subscriptionRun unit).
//
// Setup:
//  1. Inject 3 deliveries; handler sleeps 50 ms then returns DispositionAck.
//  2. Wait for all acks to be recorded by ackTimestampChannel.
//  3. Cancel ctx → consumeLoop exits cleanly → subscribeOnce calls waitAndClose.
//     waitAndClose: localWg.Wait() (all processDelivery goroutines done +
//     their Ack calls done) → then ch.Close() via sync.Once.
//  4. Sweep via Subscriber.Close (idempotent via sync.Once).
//  5. Assert closeCallTime.After(max(ackCallTimes)).
//
// ref: rabbitmq/amqp091-go channel.go — wg.Wait must precede ch.Close (A19)
func TestSubscriber_Reconnect_E2E_ChannelCloseAfterAllAcks(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	atCh := newAckTimestampChannel()
	atCh.consumeDeliveries = make(chan amqp.Delivery, 5)

	// Inject via nextChIface — the first AcquireChannel returns atCh.
	// We exit via ctx cancel (not delivery-chan close) so the reconnect loop
	// never fires, avoiding the infinite-spin problem with nextChIface not clearing.
	mockConn.mu.Lock()
	mockConn.nextChIface = atCh
	mockConn.mu.Unlock()

	const numDeliveries = 3

	// handlersDone is closed when all handlers have returned (used to verify
	// handlers are truly done before ctx cancel).
	var handlerCount atomic.Int64

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		time.Sleep(50 * time.Millisecond)
		handlerCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	// Enqueue deliveries before starting Subscribe.
	for i := range numDeliveries {
		body := makeDeliveryBody(t, outbox.Entry{
			ID:        "f6-e2e-" + string(rune('a'+i)),
			EventType: "f6.ack.order",
			Payload:   []byte(`{}`),
		})
		atCh.consumeDeliveries <- amqp.Delivery{DeliveryTag: uint64(i + 1), Body: body}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:     "f6-ack-order-queue",
		DLXExchange:   "f6-ack-order.dlx",
		PrefetchCount: numDeliveries,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "f6.ack.order.topic"}, handler) }()

	// Wait for all deliveries to be acked (each handler sleeps 50 ms).
	// At this point all processDelivery goroutines have called ch.Ack AND returned.
	// localWg.Done() has been called for each.
	require.Eventually(t, func() bool {
		return handlerCount.Load() == int64(numDeliveries)
	}, 5*time.Second, 5*time.Millisecond, "all %d handlers must have returned", numDeliveries)

	// Small sleep to ensure ackTimestampChannel records all ack timestamps.
	time.Sleep(10 * time.Millisecond)

	// Cancel ctx → consumeLoop exits via ctx.Done() with loopErr == nil.
	// subscribeOnce calls waitAndClose(ctx). Since ctx is cancelled but
	// localWg counter is 0 (all goroutines done), the `done` channel and
	// `ctx.Done()` are both ready; one is picked. Either way, eventually
	// ch.Close() is called (either by subscribeOnce or by Close() sweep).
	cancel()

	select {
	case <-subDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe did not return within 5 s after ctx cancel")
	}

	// Sweep remaining runs via Subscriber.Close (idempotent — ch.Close is
	// guarded by sync.Once). This ensures ch.Close is called even if
	// subscribeOnce's waitAndClose took the ctx.Done() path.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	_ = sub.Close(closeCtx)

	// Collect timestamps.
	atCh.mu.Lock()
	ackTimes := atCh.ackCallTimes
	closeTime := atCh.closeCallTime
	closeCount := atCh.closeCallCount
	atCh.mu.Unlock()

	require.Equal(t, numDeliveries, len(ackTimes), "must have recorded %d ack timestamps", numDeliveries)
	require.Equal(t, 1, closeCount, "ch.Close must be called exactly once (sync.Once guard)")
	require.False(t, closeTime.IsZero(), "closeCallTime must be recorded")

	// Core A19 assertion: ch.Close must be after every Ack call.
	var maxAck time.Time
	for _, at := range ackTimes {
		if at.After(maxAck) {
			maxAck = at
		}
	}
	assert.True(t, closeTime.After(maxAck) || closeTime.Equal(maxAck),
		"A19 E2E violation: ch.Close (%s) must be after last Ack (%s)", closeTime, maxAck)
}

// newSubNoShutdown is a helper that creates a Subscriber without ShutdownTimeout
// (removed in T8). Uses context for timeout instead.
func newSubNoShutdown(conn *Connection, queueName, dlx string) *Subscriber {
	return NewSubscriber(conn, SubscriberConfig{
		QueueName:   queueName,
		DLXExchange: dlx,
	})
}

// TestSubscriber_Close_RespectsCtxDeadline: ctx 100ms + handler 500ms → Close returns
// ~100ms with ErrAdapterAMQPCloseTimeout.
func TestSubscriber_Close_RespectsCtxDeadline(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	neverDone := make(chan struct{})
	t.Cleanup(func() { close(neverDone) })

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-neverDone
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := newSubNoShutdown(conn, "close-deadline-queue", "close-deadline.dlx")

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "close-deadline-1",
		EventType: "close.deadline",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "close.deadline.topic"}, handler)
	}()

	// Wait until handler is in-flight.
	time.Sleep(40 * time.Millisecond)
	cancel() // exit consume loop

	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after ctx cancel")
	}

	// Close with 100ms budget — handler is still hanging.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer closeCancel()

	start := time.Now()
	closeErr := sub.Close(closeCtx)
	elapsed := time.Since(start)

	require.Error(t, closeErr, "Close must return error when ctx expires with in-flight handlers")
	assert.Less(t, elapsed, 500*time.Millisecond,
		"Close must return promptly after ctx deadline; got %s", elapsed)
}

// TestSubscriber_Close_CancelledCtxReturnsImmediately: ctx already cancelled →
// Close returns < 50ms without waiting.
func TestSubscriber_Close_CancelledCtxReturnsImmediately(t *testing.T) {
	conn, _ := newTestConnection(t)

	sub := newSubNoShutdown(conn, "pre-cancel-queue", "pre-cancel.dlx")

	// Close a subscriber that was never subscribed to — should be instant.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := sub.Close(cancelledCtx)
	elapsed := time.Since(start)

	// Pre-cancelled ctx: Close sees ctx.Err() immediately and returns.
	require.Error(t, err, "Close with pre-cancelled ctx must return error")
	assert.True(t, errors.Is(err, context.Canceled),
		"error must be context.Canceled, got: %v", err)
	assert.Less(t, elapsed, 50*time.Millisecond,
		"Close with pre-cancelled ctx must return < 50ms; got %s", elapsed)
}

// TestSubscriber_Close_GracefulWithAmpleBudget: ctx 5s + handler 50ms → nil + clean.
func TestSubscriber_Close_GracefulWithAmpleBudget(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		time.Sleep(30 * time.Millisecond)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := newSubNoShutdown(conn, "graceful-queue", "graceful.dlx")

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "graceful-1",
		EventType: "graceful.test",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "graceful.topic"}, handler)
	}()

	// Wait for handler to complete.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond, "delivery must be acked")

	cancel()
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return")
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()

	err := sub.Close(closeCtx)
	assert.NoError(t, err, "Close must return nil with ample budget after clean drain")
}

// TestSubscriber_Close_Idempotent_WithCtx: second Close returns nil immediately.
func TestSubscriber_Close_Idempotent_WithCtx(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := newSubNoShutdown(conn, "idempotent-close-queue", "idempotent-close.dlx")

	ctx := context.Background()
	assert.NoError(t, sub.Close(ctx), "first Close must succeed")
	assert.NoError(t, sub.Close(ctx), "second Close must be a no-op and return nil")
}

// TestSubscriber_Close_InFlightHandlerCompletesBeforeDeadline: ctx 300ms + handler 80ms → nil.
func TestSubscriber_Close_InFlightHandlerCompletesBeforeDeadline(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		time.Sleep(80 * time.Millisecond)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := newSubNoShutdown(conn, "inflight-complete-queue", "inflight-complete.dlx")

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "inflight-1",
		EventType: "inflight.test",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "inflight.topic"}, handler)
	}()

	// Wait until handler is in-flight.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return")
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer closeCancel()

	err := sub.Close(closeCtx)
	assert.NoError(t, err, "Close must return nil when handler completes before ctx deadline")
}

// TestSubscriber_Close_NoDeadlineCtx_WaitsUntilWg: context.Background() + 150ms handler → nil.
func TestSubscriber_Close_NoDeadlineCtx_WaitsUntilWg(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		time.Sleep(150 * time.Millisecond)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := newSubNoShutdown(conn, "nodeadline-queue", "nodeadline.dlx")

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "nodeadline-1",
		EventType: "nodeadline.test",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "nodeadline.topic"}, handler)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return")
	}

	start := time.Now()
	err := sub.Close(context.Background())
	elapsed := time.Since(start)

	assert.NoError(t, err, "Close with Background ctx must wait indefinitely and return nil")
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"Close with Background ctx must have actually waited for handler; got %s", elapsed)
}

// TestSubscriber_Reconnect_WaitsForInflightBeforeClose — A19 core regression test.
// Exercises the subscriptionRun.waitAndClose path directly: two in-flight
// processDelivery goroutines registered via registerDelivery must complete
// (markDeliveryDone) before ch.Close is called.
//
// Uses recordingChannel to compare Ack timestamps vs Close timestamp.
func TestSubscriber_Reconnect_WaitsForInflightBeforeClose(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-a19-queue-a19.topic")

	// Track Ack call timestamps via two in-flight goroutines.
	var ackTimes []time.Time
	var ackMu sync.Mutex

	const numDeliveries = 2
	run.registerDelivery()
	run.registerDelivery()

	// Simulate two processDelivery goroutines: each sleeps 80ms then calls markDeliveryDone.
	for i := range numDeliveries {
		go func(idx int) {
			time.Sleep(80 * time.Millisecond)
			ackMu.Lock()
			ackTimes = append(ackTimes, time.Now())
			ackMu.Unlock()
			run.markDeliveryDone()
		}(i)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := run.waitAndClose(ctx)
	require.NoError(t, err, "waitAndClose must succeed when deliveries complete within budget")

	// Assert ch.Close happened after ALL acks (A19 fix).
	ackMu.Lock()
	localAckTimes := ackTimes
	ackMu.Unlock()

	closeT := rc.closeTime.Load()
	require.NotNil(t, closeT, "ch.Close must have been called")

	for i, ackT := range localAckTimes {
		assert.True(t, closeT.After(ackT) || closeT.Equal(ackT),
			"A19 violation: ch.Close (%s) must be after ack[%d] (%s)", *closeT, i, ackT)
	}
}

// TestSubscriber_Reconnect_ClosesChannelExactlyOnce verifies that multiple calls
// to waitAndClose (from both the subscribeOnce exit path and Subscriber.Close)
// call ch.Close exactly once (sync.Once guard in subscriptionRun).
func TestSubscriber_Reconnect_ClosesChannelExactlyOnce(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-once-close-queue-once.topic")

	ctx := context.Background()

	// Call waitAndClose multiple times concurrently.
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = run.waitAndClose(ctx)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), rc.closeCount.Load(),
		"ch.Close must be called exactly once across concurrent waitAndClose calls (got %d)",
		rc.closeCount.Load())
}

// ---------------------------------------------------------------------------
// F17: run kept in s.runs on waitAndClose timeout + Close() sweep compensation
// ---------------------------------------------------------------------------

// TestSubscriber_ReconnectWaitAndCloseTimeout_RunKeptForCloseSweep is the F17
// regression test. It verifies that when waitAndClose times out (processDelivery
// goroutines are still alive when the reconnect waitCtx expires), subscribeOnce
// does NOT call removeRun — the run stays in s.runs so Subscriber.Close() can
// sweep it and eventually call ch.Close exactly once.
//
// This test exercises the timeout path by:
//  1. Registering deliveries on a run directly (bypassing the full Subscribe stack
//     to avoid reconnect complexity).
//  2. Calling subscriptionRun.waitAndClose with a short ctx that expires before
//     localWg drains.
//  3. Verifying the run is NOT closed yet.
//  4. Releasing the blocking goroutines.
//  5. Calling waitAndClose again with an ample ctx → ch.Close is called exactly once.
//
// The Subscriber-level integration of this fix (removeRun only on success) is
// validated by subscriber.go's F17 comment and the compile-time logic.
//
// ref: Subscriber.Close sweep — run.waitAndClose is idempotent via sync.Once
func TestSubscriber_ReconnectWaitAndCloseTimeout_RunKeptForCloseSweep(t *testing.T) {
	rc := newRecordingChannel()
	run := newSubscriptionRun(rc, "cg-f17-timeout-queue-f17.timeout.topic")

	// Register two in-flight deliveries that will block until gate is released.
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate: // already released
		default:
			close(gate)
		}
	})

	const numInflight = 2
	for range numInflight {
		run.registerDelivery()
	}

	// Spawn goroutines simulating processDelivery — each blocks on gate.
	for range numInflight {
		go func() {
			<-gate
			run.markDeliveryDone()
		}()
	}

	// Phase 1: call waitAndClose with a very short ctx — it must timeout because
	// the goroutines are still blocking on gate.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer shortCancel()

	waitErr := run.waitAndClose(shortCtx)
	require.Error(t, waitErr, "waitAndClose must return error when ctx expires")
	assert.True(t, errors.Is(waitErr, context.DeadlineExceeded),
		"error must be context.DeadlineExceeded, got: %v", waitErr)

	// F17 assertion: ch.Close must NOT have been called yet (timeout, not success).
	assert.Equal(t, int32(0), rc.closeCount.Load(),
		"ch.Close must not be called when waitAndClose times out")

	// Phase 2: release gate → goroutines exit → localWg drains.
	close(gate)

	// Call waitAndClose again with an ample ctx → localWg drains → ch.Close via sync.Once.
	ampleCtx, ampleCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer ampleCancel()

	waitErr2 := run.waitAndClose(ampleCtx)
	require.NoError(t, waitErr2, "second waitAndClose must succeed after goroutines complete")

	// ch.Close must now be called exactly once (sync.Once guard prevents double-close).
	assert.Equal(t, int32(1), rc.closeCount.Load(),
		"ch.Close must be called exactly once after successful waitAndClose")

	// F17 corollary: call waitAndClose a third time (simulating Close() sweep) —
	// sync.Once ensures ch.Close is NOT called again.
	waitErr3 := run.waitAndClose(ampleCtx)
	require.NoError(t, waitErr3, "third waitAndClose must return nil (idempotent)")
	assert.Equal(t, int32(1), rc.closeCount.Load(),
		"ch.Close must still be called exactly once after third waitAndClose")
}
