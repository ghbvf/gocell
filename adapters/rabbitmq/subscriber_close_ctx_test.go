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
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
)

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
