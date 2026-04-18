package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// makeDeliveryBodyWithID constructs a WireMessage-envelope body where the
// entry ID is replaced by the given id string. Used to test the entry.ID guard.
func makeDeliveryBodyWithID(t *testing.T, id string) []byte {
	t.Helper()
	entry := outbox.Entry{
		ID:        id,
		EventType: "test.event",
		Payload:   []byte(`{}`),
	}
	return makeDeliveryBody(t, entry)
}

// TestProcessDelivery_LegacyEnvelopeFormat_RejectsToDLX verifies that a legacy
// (non-v1 envelope) delivery is Nacked without requeue and the handler is
// never called. After P1-14 (A2), unmarshalDelivery rejects any body that is
// not a v1 envelope — ErrUnknownEnvelopeVersion routes to DLX, not retry.
func TestProcessDelivery_LegacyEnvelopeFormat_RejectsToDLX(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	handlerCalled := false
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a non-v1 body (legacy outbox.Entry JSON format, missing schemaVersion).
	// After P1-14 (A2), unmarshalDelivery rejects any body that is not a v1
	// envelope — ErrUnknownEnvelopeVersion is returned and processDelivery
	// NACKs without requeue. The empty-ID case is now subsumed by the schema
	// version check, but the behavior (NACK, no handler call) is unchanged.
	// Note: outbox.Entry has no json tags so PascalCase field names are used.
	body := []byte(`{"ID":"","EventType":"test.event","Payload":"e30="}`)

	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 7, Body: body}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called in time")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	nackRequeue := ch.nackRequeue
	nackTag := ch.nackTag
	ch.mu.Unlock()

	assert.False(t, nackRequeue, "empty entry.ID must Nack without requeue")
	assert.Equal(t, uint64(7), nackTag)
	assert.False(t, handlerCalled, "handler must not be called for empty entry.ID")
}

// TestProcessDelivery_TooLongEntryID_RejectsToDLX verifies that an entry whose
// ID exceeds maxEntryIDLength (255) is Nacked without requeue and the handler
// is never called.
func TestProcessDelivery_TooLongEntryID_RejectsToDLX(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	handlerCalled := false
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Build an ID of exactly 256 bytes (maxEntryIDLength + 1).
	tooLongID := strings.Repeat("x", 256)
	body := makeDeliveryBodyWithID(t, tooLongID)

	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 8, Body: body}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called in time for too-long ID")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	nackRequeue := ch.nackRequeue
	nackTag := ch.nackTag
	ch.mu.Unlock()

	assert.False(t, nackRequeue, "too-long entry.ID must Nack without requeue")
	assert.Equal(t, uint64(8), nackTag)
	assert.False(t, handlerCalled, "handler must not be called for too-long entry.ID")
}

// ---------------------------------------------------------------------------
// Commit→Ack ordering tests (Commit 2, Layer 2 hard fence)
// ---------------------------------------------------------------------------

// TestProcessDelivery_CommitFailsAfterLeaseLost_NacksRequeue verifies Layer 2
// hard fence: if Receipt.Commit fails (e.g., lease expired, token mismatch),
// processDelivery must Nack(requeue=true) and NOT call ch.Ack.
func TestProcessDelivery_CommitFailsAfterLeaseLost_NacksRequeue(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{commitErr: errors.New("lease expired: token mismatch")}

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionAck,
			Receipt:     receipt,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "evt-commit-fail-1",
		EventType: "test.event",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 10, Body: body}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	// Wait for Nack to be called (Commit fails → Nack requeue=true).
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called after Commit failure")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	ackCalled := ch.ackCalled
	nackRequeue := ch.nackRequeue
	nackTag := ch.nackTag
	ch.mu.Unlock()

	assert.False(t, ackCalled, "ch.Ack must NOT be called when Commit fails")
	assert.True(t, nackRequeue, "Nack must requeue=true when Commit fails")
	assert.Equal(t, uint64(10), nackTag)

	receipt.mu.Lock()
	commitCalled := receipt.commitCalled
	receipt.mu.Unlock()
	assert.True(t, commitCalled, "Receipt.Commit must be called before broker Ack attempt")
}

// TestProcessDelivery_CommitSuccess_AcksAndDoesNotRelease verifies that when
// Receipt.Commit succeeds, ch.Ack is called and Receipt.Release is NOT called.
func TestProcessDelivery_CommitSuccess_AcksAndDoesNotRelease(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{} // commitErr = nil → success

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionAck,
			Receipt:     receipt,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "evt-commit-ok-1",
		EventType: "test.event",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 11, Body: body}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond, "Ack was not called after successful Commit")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	ackTag := ch.ackTag
	ch.mu.Unlock()

	assert.Equal(t, uint64(11), ackTag)

	receipt.mu.Lock()
	commitCalled := receipt.commitCalled
	releaseCalled := receipt.releaseCalled
	receipt.mu.Unlock()

	assert.True(t, commitCalled, "Receipt.Commit must be called on DispositionAck")
	assert.False(t, releaseCalled, "Receipt.Release must NOT be called on successful Commit+Ack")
}

// ---------------------------------------------------------------------------
// Commit 5: real concurrency tests (A13)
// ---------------------------------------------------------------------------

// TestSubscriber_PrefetchCount10_RealConcurrency verifies that consumeLoop
// launches processDelivery in goroutines, allowing PrefetchCount deliveries to
// be processed concurrently. Under the old serial dispatch the WaitGroup barrier
// below would never be reached within the 500ms budget → the test would timeout.
//
// ref: ThreeDotsLabs/watermill message/router.go h.run — per-message goroutine
func TestSubscriber_PrefetchCount10_RealConcurrency(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	// Provide 11 channels: 1 for AcquireChannel in subscribeOnce, then any extras
	// that may be needed. Using channelQueue so each call gets a distinct channel.
	ch := newMockChannel()
	// Increase buffer so all 10 deliveries fit without blocking the send loop.
	ch.consumeDeliveries = make(chan amqp.Delivery, 10)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	const numDeliveries = 10
	const barrierTimeout = 500 * time.Millisecond

	// barrier: each goroutine calls wg.Done() when it enters the handler.
	// The test thread calls wg.Wait() to verify all 10 are concurrent.
	var barrier sync.WaitGroup
	barrier.Add(numDeliveries)

	// blockCh allows handlers to proceed after the barrier is reached.
	blockCh := make(chan struct{})

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		barrier.Done() // signal arrival
		<-blockCh      // wait until all have arrived
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Enqueue 10 deliveries before starting subscriber so they're all ready.
	for i := range numDeliveries {
		body := makeDeliveryBody(t, outbox.Entry{
			ID:        fmt.Sprintf("evt-concurrent-%d", i),
			EventType: "test.concurrent",
			Payload:   []byte(`{}`),
		})
		ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: uint64(i + 1), Body: body}
	}

	subDone := make(chan error, 1)
	go func() {
		subDone <- NewSubscriber(conn, SubscriberConfig{
			QueueName:       "test-queue",
			DLXExchange:     "test.dlx",
			PrefetchCount:   numDeliveries,
			ShutdownTimeout: 2 * time.Second,
		}).Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler)
	}()

	// If concurrent: all 10 handlers reach the barrier within barrierTimeout.
	// If serial: only 1 reaches the barrier (blocked by blockCh), test times out.
	barrierDone := make(chan struct{})
	go func() {
		barrier.Wait()
		close(barrierDone)
	}()

	select {
	case <-barrierDone:
		// All 10 goroutines entered the handler concurrently — pass.
	case <-time.After(barrierTimeout):
		t.Fatal("consumeLoop is serial: not all 10 deliveries reached handler concurrently within 500ms")
	}

	close(blockCh) // unblock handlers
	cancel()
	assert.NoError(t, <-subDone)
}

// TestSubscriber_ConcurrentReceiptCommitSafety verifies that when 10
// deliveries are processed concurrently, every Receipt.Commit is called
// exactly once (no loss, no duplicate).
//
// ref: rabbitmq/amqp091-go channel.go — ch.Ack/Nack guarded by internal mutex
func TestSubscriber_ConcurrentReceiptCommitSafety(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	const numDeliveries = 10

	var commitCount atomic.Int64

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, numDeliveries)

	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		receipt := &countingReceipt{counter: &commitCount}
		return outbox.HandleResult{
			Disposition: outbox.DispositionAck,
			Receipt:     receipt,
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := range numDeliveries {
		body := makeDeliveryBody(t, outbox.Entry{
			ID:        fmt.Sprintf("evt-safety-%d", i),
			EventType: "test.safety",
			Payload:   []byte(`{}`),
		})
		ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: uint64(i + 1), Body: body}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		PrefetchCount:   numDeliveries,
		ShutdownTimeout: 2 * time.Second,
	})

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler)
	}()

	// Wait until all 10 commits have been recorded.
	require.Eventually(t, func() bool {
		return commitCount.Load() == int64(numDeliveries)
	}, 3*time.Second, 5*time.Millisecond, "expected %d Receipt.Commit calls", numDeliveries)

	cancel()
	assert.NoError(t, <-subDone)

	// Assert Commit count == numDeliveries (no loss, no double-commit).
	assert.Equal(t, int64(numDeliveries), commitCount.Load(),
		"each delivery must have Receipt.Commit called exactly once")
}

// countingReceipt is a Receipt that increments an atomic counter on Commit.
type countingReceipt struct {
	counter *atomic.Int64
}

func (r *countingReceipt) Commit(_ context.Context) error {
	r.counter.Add(1)
	return nil
}

func (r *countingReceipt) Release(_ context.Context) error                 { return nil }
func (r *countingReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

var _ outbox.Receipt = (*countingReceipt)(nil)

// TestSubscriber_GoroutineLeakOnClose verifies that Close() properly waits
// for all in-flight processDelivery goroutines and leaves no residual goroutines
// owned by the Subscriber itself.
//
// The Connection's reconnectLoop is a known background goroutine managed by
// the Connection lifecycle; it is excluded via goleak.IgnoreTopFunction so
// the test focuses only on Subscriber-owned goroutines.
//
// ref: go.uber.org/goleak — goroutine leak detection
func TestSubscriber_GoroutineLeakOnClose(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 5)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	// Handler returns immediately so processDelivery goroutines finish quickly.
	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler)
	}()

	// Enqueue a delivery so at least one goroutine runs.
	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "evt-leak-1",
		EventType: "test.leak",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	// Wait for the delivery to be processed.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond)

	cancel()
	assert.NoError(t, <-subDone)

	// Close subscriber.
	assert.NoError(t, sub.Close())

	// Close the Connection explicitly so its reconnectLoop goroutine exits
	// before goleak.VerifyNone runs.
	assert.NoError(t, conn.Close())

	// Verify no goroutines were leaked by the Subscriber.
	// The Connection.reconnectLoop is already stopped above (conn.Close),
	// but goleak.IgnoreTopFunction is added as a belt-and-suspenders guard
	// in case the event loop hasn't fully unwound yet.
	goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("github.com/ghbvf/gocell/adapters/rabbitmq.(*Connection).reconnectLoop"),
		// testcontainers Reaper goroutine survives the parent test's lifetime by
		// design (it cleans up containers post-process); excluded so this test
		// is stable when the suite is run with -tags=integration.
		goleak.IgnoreTopFunction("github.com/testcontainers/testcontainers-go.(*Reaper).connect.func1"),
	)
}

// TestProcessDelivery_ValidEntryID_PassesToHandler verifies that an entry with
// ID at exactly the boundary length (255 bytes) passes the guard and reaches
// the handler.
func TestProcessDelivery_ValidEntryID_PassesToHandler(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	// Exactly maxEntryIDLength bytes.
	boundaryID := strings.Repeat("a", 255)

	handled := make(chan string, 1)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handled <- e.ID
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := makeDeliveryBodyWithID(t, boundaryID)
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 9, Body: body}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond, "Ack was not called in time for boundary-length ID")

	cancel()
	assert.NoError(t, <-subDone)

	select {
	case receivedID := <-handled:
		assert.Equal(t, boundaryID, receivedID, "handler must be called with exact boundary ID")
	case <-time.After(1 * time.Second):
		t.Fatal("handler was not called for valid boundary-length entry.ID")
	}

	ch.mu.Lock()
	ackTag := ch.ackTag
	ch.mu.Unlock()
	assert.Equal(t, uint64(9), ackTag)
}
