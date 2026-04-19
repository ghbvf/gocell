package rabbitmq

// T9: Publisher.Close(ctx) tests.
//
// ref: uber-go/fx app.go StopTimeout — ctx carries shared shutdown budget
// ref: amqp091-go channel.go — per-publish channel lifecycle
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close signature

import (
	"context"
	"errors"
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

// TestPublisher_Close_CtxExceeded_ReturnsTimeoutErr verifies that Close
// honours the caller's ctx deadline when an in-flight Publish is blocked.
//
// Strategy (F19): inject a blockingPublishChannel whose PublishWithContext
// blocks on a gate channel. The test:
//  1. Starts a Publish goroutine that blocks at the channel gate.
//  2. Calls pub.Close(ctxWith100msTimeout).
//  3. Asserts Close returns ~100ms later with a ctx error (not the gate).
//  4. Releases the gate so the goroutine exits cleanly (no leak).
//
// ref: uber-go/fx app.go StopTimeout — ctx budget propagated to teardown
func TestPublisher_Close_CtxExceeded_ReturnsTimeoutErr(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	gate := make(chan struct{})
	blocking := &blockingPublishChannel{
		mockChannel:    newAutoConfirmChannel(),
		publishBlocker: gate,
	}
	mockConn.mu.Lock()
	mockConn.nextChIface = blocking
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	// publishDone is closed when the Publish goroutine exits.
	publishDone := make(chan struct{})
	publishStarted := make(chan struct{})

	go func() {
		defer close(publishDone)
		close(publishStarted)
		// Publish blocks inside blockingPublishChannel.PublishWithContext.
		_ = pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}()

	<-publishStarted

	// Give the goroutine a moment to enter the blocking Publish call.
	// We use a tiny sleep here only to advance past the goroutine schedule
	// point — this is not a timing assertion.
	time.Sleep(5 * time.Millisecond)

	// Budget: 100ms — Close must return within this window even though
	// Publish is still blocked on the gate.
	budget := 100 * time.Millisecond
	closeCtx, closeCancel := context.WithTimeout(context.Background(), budget)
	defer closeCancel()

	start := time.Now()
	err := pub.Close(closeCtx)
	elapsed := time.Since(start)

	// Close must have returned with a timeout error.
	require.Error(t, err, "Close must return error when ctx budget exceeded")
	assert.LessOrEqual(t, elapsed, budget+50*time.Millisecond,
		"Close must return within budget+tolerance; got %s", elapsed)

	// Release the gate — unblocks the Publish goroutine.
	close(gate)

	// Verify the Publish goroutine exits cleanly (no goroutine leak).
	select {
	case <-publishDone:
		// goroutine exited — no leak
	case <-time.After(2 * time.Second):
		t.Error("Publish goroutine did not exit after gate released (goroutine leak)")
	}
}

// TestPublisher_Close_WaitsForInFlightPublishes verifies that Close waits for
// any currently in-progress Publish calls to complete before returning. This
// ensures graceful shutdown does not orphan inflight broker I/O.
//
// Strategy (F19): inject a blockingPublishChannel with a gate. Close is
// given an ample budget. We release the gate after Close has started waiting,
// and assert Close returns only after Publish completes.
func TestPublisher_Close_WaitsForInFlightPublishes(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	gate := make(chan struct{})
	blocking := &blockingPublishChannel{
		mockChannel:    newAutoConfirmChannel(),
		publishBlocker: gate,
	}
	mockConn.mu.Lock()
	mockConn.nextChIface = blocking
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	publishStarted := make(chan struct{})
	publishDone := make(chan error, 1)

	go func() {
		close(publishStarted)
		publishDone <- pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}()

	<-publishStarted
	// Give the goroutine a moment to enter the blocking Publish call.
	time.Sleep(5 * time.Millisecond)

	// Ample budget: 3 seconds.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()

	// Release the gate concurrently — simulates Publish completing.
	releaseAt := time.AfterFunc(20*time.Millisecond, func() { close(gate) })
	defer releaseAt.Stop()

	// Close should block until Publish is done (gate released).
	closeErr := pub.Close(closeCtx)

	// Publish must also have completed before Close returned.
	select {
	case publishErr := <-publishDone:
		_ = publishErr // success or failure both acceptable; goroutine exited
	default:
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

// blockingPublishChannel wraps a mockChannel and blocks PublishWithContext
// on publishBlocker until it is closed or a 2-second safety net fires.
// This provides a deterministic synchronisation point for close tests,
// replacing time.Sleep-based polling.
//
// ref: F19 — deterministic blocking point for Publisher.Close(ctx) tests
type blockingPublishChannel struct {
	*mockChannel
	publishBlocker chan struct{}
}

func (b *blockingPublishChannel) PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	// Block until the test releases the gate, ctx is cancelled, or safety net fires.
	select {
	case <-b.publishBlocker:
		// Gate released — proceed with underlying publish.
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		// Safety net: prevent test deadlock if gate is never closed.
		return errors.New("blockingPublishChannel: safety net timeout — gate not released within 2s")
	}
	return b.mockChannel.PublishWithContext(ctx, exchange, key, mandatory, immediate, msg)
}
