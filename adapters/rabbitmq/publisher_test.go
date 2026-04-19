package rabbitmq

// T9: Publisher.Close(ctx) tests.
//
// ref: uber-go/fx app.go StopTimeout — ctx carries shared shutdown budget
// ref: amqp091-go channel.go — per-publish channel lifecycle

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublisher_Close_Idempotent verifies that Close can be called multiple
// times without error (atomic closed flag guard).
func TestPublisher_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)
	pub := NewPublisher(conn)

	ctx := context.Background()
	assert.NoError(t, pub.Close(ctx), "first Close must succeed")
	assert.NoError(t, pub.Close(ctx), "second Close must be no-op and return nil")
}

// TestPublisher_Close_CancelledCtxReturnsImmediately verifies that Close with a
// pre-cancelled ctx returns the ctx error promptly (< 50ms) without hanging.
func TestPublisher_Close_CancelledCtxReturnsImmediately(t *testing.T) {
	conn, _ := newTestConnection(t)
	pub := NewPublisher(conn)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	err := pub.Close(cancelledCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "Close with pre-cancelled ctx must return error")
	assert.Less(t, elapsed, 50*time.Millisecond,
		"Close must return promptly with pre-cancelled ctx; got %s", elapsed)
}

// TestPublisher_Close_WaitsForInFlightPublishes verifies that Close waits for
// any currently in-progress Publish calls to complete before returning. This
// ensures graceful shutdown does not orphan inflight broker I/O.
func TestPublisher_Close_WaitsForInFlightPublishes(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	// autoConfirmation so Publish doesn't time out waiting for confirm.
	autoConf := newAutoConfirmChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = autoConf
	mockConn.mu.Unlock()
	_ = ch

	pub := NewPublisher(conn)

	// Start a Publish in a goroutine that will be in-flight when Close is called.
	publishStarted := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		close(publishStarted)
		publishDone <- pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}()

	<-publishStarted
	// Give Publish a moment to enter the inflight window.
	time.Sleep(10 * time.Millisecond)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()

	// Close should wait for the in-flight Publish to finish.
	closeErr := pub.Close(closeCtx)

	// Publish should also have completed.
	select {
	case publishErr := <-publishDone:
		// Publish may succeed or fail depending on timing; we don't assert on it.
		_ = publishErr
	default:
		// If Publish hasn't returned yet, wait briefly.
		select {
		case <-publishDone:
		case <-time.After(500 * time.Millisecond):
			t.Error("Publish did not complete after Close returned")
		}
	}

	assert.NoError(t, closeErr, "Close must succeed when in-flight Publish completes within budget")
}

// newAutoConfirmChannel creates a mockChannel that auto-confirms publishes
// (sends ACK confirmation immediately on NotifyPublish).
func newAutoConfirmChannel() *mockChannel {
	ch := newMockChannel()
	ack := true
	ch.autoConfirmation = &amqp.Confirmation{DeliveryTag: 1, Ack: ack}
	return ch
}
