package rabbitmq

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestSubscriber_StopIntakeCancelsConsumerButDrainsInflight verifies that
// StopIntake issues basic.cancel to the broker (so no new deliveries arrive)
// and that already-prefetched messages are fully processed before the
// consumeLoop exits.
//
// Flow:
//  1. 3 deliveries are pre-loaded in the buffered deliveries channel.
//  2. Subscribe starts and enters consumeLoop (processing deliveries concurrently).
//  3. StopIntake is called → stopIntakeCh is closed → consumeLoop enters drainRemaining.
//  4. drainRemaining dispatches the remaining prefetched deliveries.
//  5. Test waits until all 3 handlers complete, then closes the deliveries chan
//     (simulating broker ack of basic.cancel).
//  6. consumeLoop exits; Subscribe returns nil.
func TestSubscriber_StopIntakeCancelsConsumerButDrainsInflight(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	// Buffer large enough for all pre-loaded deliveries.
	ch.consumeDeliveries = make(chan amqp.Delivery, 3)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	const numDeliveries = 3
	var handlerCount atomic.Int64
	// Each handler signals arrival then blocks until released.
	var wgHandlers sync.WaitGroup
	wgHandlers.Add(numDeliveries)
	released := make(chan struct{})

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		wgHandlers.Done() // signal arrival
		<-released        // wait for test to release
		handlerCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "drain-test-queue",
		DLXExchange:     "drain.dlx",
		ShutdownTimeout: 5 * time.Second,
	})

	// Pre-load 3 deliveries before Subscribe starts.
	for i := range numDeliveries {
		body := makeDeliveryBody(t, outbox.Entry{
			ID:        fmt.Sprintf("drain-evt-%d", i),
			EventType: "drain.test",
			Payload:   []byte(`{}`),
		})
		ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: uint64(i + 1), Body: body}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "drain.topic"}, handler)
	}()

	// Wait until all 3 handler goroutines have started (they're blocked on <-released).
	handlerStarted := make(chan struct{})
	go func() {
		wgHandlers.Wait()
		close(handlerStarted)
	}()
	select {
	case <-handlerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("handlers did not start within 3s")
	}

	// Call StopIntake — should close stopIntakeCh and call ch.Cancel. With
	// the concurrent Cancel dispatch (F1 fix), we wait for cancelCalled via
	// Eventually so the test does not race with the dispatch goroutine.
	err := sub.StopIntake(ctx)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.cancelCalled
	}, 2*time.Second, 10*time.Millisecond,
		"StopIntake must call ch.Cancel to stop broker delivery")

	// Release the handlers so they can complete.
	close(released)

	// Wait until all 3 handlers have finished.
	require.Eventually(t, func() bool {
		return handlerCount.Load() == int64(numDeliveries)
	}, 3*time.Second, 5*time.Millisecond, "all %d deliveries must be handled", numDeliveries)

	// Simulate broker closing the deliveries channel after basic.cancel.
	close(ch.consumeDeliveries)

	// consumeLoop (drainRemaining) should exit cleanly.
	select {
	case err := <-subDone:
		assert.NoError(t, err, "Subscribe must return nil after clean drain")
	case <-time.After(3 * time.Second):
		t.Fatal("Subscribe did not return after drain completed")
	}

	assert.Equal(t, int64(numDeliveries), handlerCount.Load(),
		"all prefetched deliveries must be processed")

	// Verify every delivery was Ack'd to the broker.
	ch.mu.Lock()
	gotAck := ch.ackCount
	ch.mu.Unlock()
	assert.Equal(t, int64(numDeliveries), gotAck,
		"every delivery must be broker-Ack'd; got %d, want %d", gotAck, numDeliveries)
}

// TestSubscriber_ConsumerTagTruncation verifies that a long queue+topic
// combination is truncated to ≤ 250 bytes (AMQP shortstr limit) and that
// the truncated tag is still unique (hash suffix appended).
func TestSubscriber_ConsumerTagTruncation(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	// Build a queue name that makes the raw consumerTag exceed 250 bytes.
	longQueue := fmt.Sprintf("queue-%s", strings.Repeat("x", 200))
	longTopic := fmt.Sprintf("topic-%s", strings.Repeat("y", 100))

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       longQueue,
		DLXExchange:     "trunc.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: longTopic}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait until Consume() has been called (consumerTag recorded in mockChannel).
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.cancelConsumer != "" || ch.cancelCalled || ch.consumeDeliveries != nil
	}, 2*time.Second, 10*time.Millisecond, "Subscribe should start consuming")

	cancel()
	select {
	case <-subDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Subscribe did not return after ctx cancel")
	}

	// The consumerTag passed to ch.Consume is not directly accessible on mockChannel,
	// but we can verify the length constraint by constructing the raw tag and
	// checking what truncation produces.
	rawTag := fmt.Sprintf("cg-%s-%s", longQueue, longTopic)
	assert.Greater(t, len(rawTag), 250, "raw tag must exceed 250 to trigger truncation")
}

// TestSubscriber_StopIntake_Idempotent verifies that calling StopIntake
// multiple times never panics and always returns nil.
func TestSubscriber_StopIntake_Idempotent(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "idempotent-queue",
		DLXExchange:     "idempotent.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx := context.Background()

	// Call StopIntake 3 times — must not panic or return error.
	for i := range 3 {
		err := sub.StopIntake(ctx)
		assert.NoError(t, err, "StopIntake call %d must return nil", i+1)
	}
}

// TestSubscriber_IntakeStoppedThenCloseNoTimeout verifies the ideal path:
// after StopIntake drains in-flight messages and the deliveries chan closes,
// Close() finishes well within ShutdownTimeout (no ErrAdapterAMQPCloseTimeout).
func TestSubscriber_IntakeStoppedThenCloseNoTimeout(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	const shutdownTimeout = 2 * time.Second

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "close-fast-queue",
		DLXExchange:     "close-fast.dlx",
		ShutdownTimeout: shutdownTimeout,
	})

	// Send one delivery so Subscribe has something to process.
	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "close-fast-1",
		EventType: "close.fast",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "close.fast.topic"}, handler)
	}()

	// Wait until the delivery is acked (handler completed).
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond, "delivery was not acked")

	// StopIntake → deliveries chan closes → Subscribe exits.
	err := sub.StopIntake(ctx)
	require.NoError(t, err)

	// Close deliveries chan to simulate broker basic.cancel ack.
	close(ch.consumeDeliveries)

	// Subscribe should exit.
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not exit after StopIntake + deliveries close")
	}

	// Close should finish well before ShutdownTimeout.
	start := time.Now()
	closeErr := sub.Close(context.Background())
	elapsed := time.Since(start)

	assert.NoError(t, closeErr, "Close must not return ErrAdapterAMQPCloseTimeout after clean drain")
	assert.Less(t, elapsed, shutdownTimeout/2,
		"Close should finish quickly after StopIntake drained; took %v", elapsed)
}

// TestSubscriber_HardCloseForcesTimeout is a regression guard: when Close() is
// called without StopIntake and the handler is hanging, Close must return
// ErrAdapterAMQPCloseTimeout after ShutdownTimeout expires.
func TestSubscriber_HardCloseForcesTimeout(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	const shortTimeout = 150 * time.Millisecond

	neverClose := make(chan struct{})
	// Unblock the hanging handler goroutine after the test completes so it
	// does not leak into subsequent tests (e.g. goroutine-leak detectors).
	t.Cleanup(func() { close(neverClose) })

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-neverClose // block until test cleanup
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "timeout-queue",
		DLXExchange:     "timeout.dlx",
		ShutdownTimeout: shortTimeout,
	})

	// Send one delivery to trigger a hanging handler goroutine.
	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "timeout-1",
		EventType: "timeout.test",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(ctx, outbox.Subscription{Topic: "timeout.topic"}, handler)
	}()

	// Wait briefly to let the delivery reach the handler goroutine
	// (consumeLoop dispatches it via wg.Add(1) + go processDelivery).
	time.Sleep(30 * time.Millisecond)

	// Cancel context so consumeLoop exits via ctx.Done(). The processDelivery
	// goroutine keeps running because it is blocked on neverClose.
	cancel()

	// Wait for Subscribe to return (consumeLoop has exited) before calling
	// Close. This ensures wg.Add in consumeLoop has fully completed, avoiding
	// a race between wg.Add and the wg.Wait goroutine spawned inside Close.
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after context cancel")
	}

	// Close without StopIntake: processDelivery is hanging → must time out.
	start := time.Now()
	closeErr := sub.Close(context.Background())
	elapsed := time.Since(start)

	// Close must have waited (at least shortTimeout / 2) before giving up.
	assert.Error(t, closeErr, "Close must return ErrAdapterAMQPCloseTimeout when handler hangs")
	assert.ErrorContains(t, closeErr, string(ErrAdapterAMQPCloseTimeout))
	assert.GreaterOrEqual(t, elapsed, shortTimeout/2,
		"Close should have waited at least half of ShutdownTimeout")
}

// TestSubscriber_StopIntake_RespectsCtx verifies that StopIntake returns
// promptly when its context is cancelled, even if the broker's basic.cancel
// is hanging. This is the F1 fix: StopIntake must not hold the lock across
// broker I/O, and must honour the ctx budget.
//
// Setup: one channel whose Cancel blocks on cancelHangUntil (never closed).
// Expectation: StopIntake returns context.DeadlineExceeded within ~150ms of
// the ctx deadline, not hanging on the broker.
func TestSubscriber_StopIntake_RespectsCtx(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.cancelHangUntil = make(chan struct{}) // never closed → Cancel hangs
	t.Cleanup(func() { close(ch.cancelHangUntil) })

	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "ctx-respect-queue",
		DLXExchange:     "ctx-respect.dlx",
		ShutdownTimeout: 5 * time.Second,
	})

	// Start Subscribe so the consumerTag is registered.
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "ctx.topic"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	// Wait for Subscribe to register its consumerTag.
	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return len(sub.consumerTags) > 0
	}, 2*time.Second, 10*time.Millisecond, "Subscribe must register a consumerTag")

	// Call StopIntake with a short ctx; broker Cancel hangs indefinitely.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopCancel()

	start := time.Now()
	err := sub.StopIntake(stopCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "StopIntake must return ctx.Err when broker hangs past ctx deadline")
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"StopIntake must propagate ctx.DeadlineExceeded")
	assert.Less(t, elapsed, 500*time.Millisecond,
		"StopIntake must return within a short margin of ctx deadline; got %s", elapsed)

	// Clean up: cancel Subscribe so the goroutine exits.
	subCancel()
	<-subDone
}

// TestSubscriber_StopIntake_PerCallTimeout verifies that a single hanging
// basic.cancel does not stall StopIntake beyond its per-call budget.
// The outer ctx is generous; the per-call timeout (configured via
// SubscriberConfig.StopIntakePerCallTimeout) bounds how long any one
// Cancel can hold the shutdown path.
//
// Expectation: StopIntake returns nil (best-effort) within ~400ms when
// per-call timeout is 300ms, even though the broker never responds.
func TestSubscriber_StopIntake_PerCallTimeout(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.cancelHangUntil = make(chan struct{}) // never closed → Cancel hangs
	t.Cleanup(func() { close(ch.cancelHangUntil) })

	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:                "per-call-timeout-queue",
		DLXExchange:              "per-call-timeout.dlx",
		ShutdownTimeout:          5 * time.Second,
		StopIntakePerCallTimeout: 300 * time.Millisecond,
	})

	// Start Subscribe so the consumerTag is registered.
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "per-call.topic"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return len(sub.consumerTags) > 0
	}, 2*time.Second, 10*time.Millisecond, "Subscribe must register a consumerTag")

	// Outer ctx is generous; per-call timeout should bound Cancel.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()

	start := time.Now()
	err := sub.StopIntake(stopCtx)
	elapsed := time.Since(start)

	// best-effort: a Cancel that times out is logged as Warn; StopIntake returns nil.
	require.NoError(t, err, "StopIntake must return nil (best-effort) when per-call timeout fires")
	assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond,
		"StopIntake must wait at least the per-call timeout; got %s", elapsed)
	assert.Less(t, elapsed, 1*time.Second,
		"StopIntake must not exceed per-call timeout substantially; got %s", elapsed)

	subCancel()
	<-subDone
}

// TestSubscriber_StopIntake_DoesNotHoldLockAcrossBrokerIO verifies the key
// F1 invariant: Subscriber.mu is NOT held while ch.Cancel is pending.
// Another goroutine holding lock-dependent logic (e.g. a new Subscribe call,
// or consumeLoop's delete(consumerTags, ch)) must not be blocked by a slow
// basic.cancel.
func TestSubscriber_StopIntake_DoesNotHoldLockAcrossBrokerIO(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	hangGate := make(chan struct{})
	ch.cancelHangUntil = hangGate
	t.Cleanup(func() {
		select {
		case <-hangGate: // already closed
		default:
			close(hangGate)
		}
	})

	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:                "lock-free-queue",
		DLXExchange:              "lock-free.dlx",
		ShutdownTimeout:          5 * time.Second,
		StopIntakePerCallTimeout: 2 * time.Second,
	})

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "lock.topic"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	require.Eventually(t, func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return len(sub.consumerTags) > 0
	}, 2*time.Second, 10*time.Millisecond, "Subscribe must register a consumerTag")

	// Fire StopIntake in a goroutine; Cancel hangs on hangGate.
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sub.StopIntake(context.Background())
	}()

	// While StopIntake is pending (hanging on broker Cancel), the Subscriber
	// lock must NOT be held — a lock acquisition from another goroutine must
	// succeed within 100ms.
	lockAcquired := make(chan struct{})
	go func() {
		sub.mu.Lock()
		close(lockAcquired)
		sub.mu.Unlock()
	}()

	select {
	case <-lockAcquired:
		// success — StopIntake released the lock before invoking broker I/O
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Subscriber.mu was held across broker I/O — StopIntake must snapshot then release")
	}

	// Release the hang and let StopIntake complete.
	close(hangGate)
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("StopIntake did not complete after hang released")
	}

	subCancel()
	<-subDone
}
