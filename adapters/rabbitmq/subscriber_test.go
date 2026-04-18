package rabbitmq

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestProcessDelivery_EmptyEntryID_RejectsToDLX verifies that an entry with
// an empty ID is Nacked without requeue and the handler is never called.
func TestProcessDelivery_EmptyEntryID_RejectsToDLX(t *testing.T) {
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

	// Use a legacy outbox.Entry JSON body with empty ID. The unmarshalDelivery
	// legacy fallback parses this successfully (JSON is well-formed), and the
	// entry.ID guard in processDelivery then Nacks it without requeue.
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
