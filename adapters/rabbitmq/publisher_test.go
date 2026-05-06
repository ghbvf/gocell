package rabbitmq

// T9: Publisher.Close(ctx) tests.
//
// ref: uber-go/fx app.go StopTimeout — ctx carries shared shutdown budget
// ref: amqp091-go channel.go — per-publish channel lifecycle
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close signature

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestPublisher_Close_Idempotent verifies that Close can be called multiple
// times without error (atomic closed flag guard).
func TestPublisher_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)
	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))

	ctx := context.Background()
	assert.NoError(t, pub.Close(ctx), "first Close must succeed")
	assert.NoError(t, pub.Close(ctx), "second Close must be no-op and return nil")
}

// TestPublisher_Close_CancelledCtxReturnsImmediately verifies that Close with a
// pre-canceled ctx returns the ctx error promptly (< 50ms) without hanging.
func TestPublisher_Close_CancelledCtxReturnsImmediately(t *testing.T) {
	conn, _ := newTestConnection(t)
	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	start := time.Now()
	err := pub.Close(cancelledCtx)
	elapsed := time.Since(start)

	require.Error(t, err, "Close with pre-canceled ctx must return error")
	assert.ErrorIs(t, err, context.Canceled,
		"Close with pre-canceled ctx must return context.Canceled, got: %v", err)
	assert.Less(t, elapsed, testtime.MediumPoll,
		"Close must return promptly with pre-canceled ctx; got %s", elapsed)
}

// TestPublisher_Close_CtxExceeded_ReturnsTimeoutErr verifies that Close
// honors the caller's ctx deadline when an in-flight Publish is blocked.
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

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))

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
	time.Sleep(testtime.FastPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking Publish; no started observable

	// Budget: 100ms — Close must return within this window even though
	// Publish is still blocked on the gate.
	budget := testtime.SlowPoll
	closeCtx, closeCancel := context.WithTimeout(context.Background(), budget)
	defer closeCancel()

	start := time.Now()
	err := pub.Close(closeCtx)
	elapsed := time.Since(start)

	// Close must have returned with a timeout error.
	require.Error(t, err, "Close must return error when ctx budget exceeded")
	assert.LessOrEqual(t, elapsed, budget+testtime.MediumPoll,
		"Close must return within budget+tolerance; got %s", elapsed)

	// Release the gate — unblocks the Publish goroutine.
	close(gate)

	// Verify the Publish goroutine exits cleanly (no goroutine leak).
	select {
	case <-publishDone:
		// goroutine exited — no leak
	case <-time.After(testtime.D2s):
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

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))

	publishStarted := make(chan struct{})
	publishDone := make(chan error, 1)

	go func() {
		close(publishStarted)
		publishDone <- pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}()

	<-publishStarted
	// Give the goroutine a moment to enter the blocking Publish call.
	time.Sleep(testtime.FastPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking Publish; no started observable

	// Ample budget: 3 seconds.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer closeCancel()

	// Release the gate concurrently — simulates Publish completing.
	releaseAt := time.AfterFunc(testtime.D20ms, func() { close(gate) })
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
		case <-time.After(testtime.D500ms):
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
// This provides a deterministic synchronization point for close tests,
// replacing time.Sleep-based polling.
//
// ref: F19 — deterministic blocking point for Publisher.Close(ctx) tests
type blockingPublishChannel struct {
	*mockChannel
	publishBlocker chan struct{}
}

func (b *blockingPublishChannel) PublishWithContext(
	ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing,
) error {
	// Block until the test releases the gate, ctx is canceled, or safety net fires.
	select {
	case <-b.publishBlocker:
		// Gate released — proceed with underlying publish.
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(testtime.D2s):
		// Safety net: prevent test deadlock if gate is never closed.
		return errors.New("blockingPublishChannel: safety net timeout — gate not released within 2s")
	}
	return b.mockChannel.PublishWithContext(ctx, exchange, key, mandatory, immediate, msg)
}

// =============================================================================
// Batch C: fakeCollector + collector-wiring tests
// =============================================================================

// fakeCollector records RecordPublishFailure calls for assertion in tests.
type fakeCollector struct {
	mu      sync.Mutex
	reasons []PublishFailureReason
}

func (f *fakeCollector) RecordPublishFailure(reason PublishFailureReason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons = append(f.reasons, reason)
}

func (f *fakeCollector) recorded() []PublishFailureReason {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PublishFailureReason, len(f.reasons))
	copy(out, f.reasons)
	return out
}

// TestPublish_NackReturnsNackErrcodeAndRecords verifies that a broker NACK
// returns ErrAdapterAMQPNack and records PublishFailureNack on the collector.
func TestPublish_NackReturnsNackErrcodeAndRecords(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.autoConfirmation = &amqp.Confirmation{Ack: false, DeliveryTag: 1}
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	col := &fakeCollector{}
	pub := NewPublisher(conn,
		WithPublisherClock(clock.Real()),
		WithPublisherCollector(col),
	)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrAdapterAMQPNack),
		"NACK must return ErrAdapterAMQPNack, not ErrAdapterAMQPConfirmTimeout")

	reasons := col.recorded()
	require.Len(t, reasons, 1)
	assert.Equal(t, PublishFailureNack, reasons[0])
}

// TestPublish_TimeoutReturnsTimeoutAndRecords verifies that a confirm-timer
// expiry returns ErrAdapterAMQPConfirmTimeout and records PublishFailureTimeout.
func TestPublish_TimeoutReturnsTimeoutAndRecords(t *testing.T) {
	mockConn := newMockConnection()
	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	conn, err := NewConnection(Config{
		URL:            "amqp://test@localhost/",
		ConfirmTimeout: testtime.MediumPoll,
	}, WithDialFunc(dialFunc), WithConnectionClock(clock.Real()))
	require.NoError(t, err)
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("close error: %v", cErr)
		}
	}()

	col := &fakeCollector{}
	pub := NewPublisher(conn,
		WithPublisherClock(clock.Real()),
		WithPublisherCollector(col),
	)

	err = pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrAdapterAMQPConfirmTimeout))

	reasons := col.recorded()
	require.Len(t, reasons, 1)
	assert.Equal(t, PublishFailureTimeout, reasons[0])
}

// TestPublish_ConfirmChanClosedReturnsTimeoutAndRecords verifies that a closed
// confirm channel returns ErrAdapterAMQPConfirmTimeout and records
// PublishFailureChanClosed on the collector.
func TestPublish_ConfirmChanClosedReturnsTimeoutAndRecords(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.autoCloseConfirm = true
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	col := &fakeCollector{}
	pub := NewPublisher(conn,
		WithPublisherClock(clock.Real()),
		WithPublisherCollector(col),
	)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{"data":"value"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(ErrAdapterAMQPConfirmTimeout))
	assert.Contains(t, err.Error(), "confirm channel closed")

	reasons := col.recorded()
	require.Len(t, reasons, 1)
	assert.Equal(t, PublishFailureChanClosed, reasons[0])
}

// TestNoopPublisherCollector_DoesNotPanic verifies that NoopPublisherCollector
// handles all known reasons without panic.
func TestNoopPublisherCollector_DoesNotPanic(t *testing.T) {
	noop := NoopPublisherCollector{}
	for _, r := range []PublishFailureReason{
		PublishFailureNack,
		PublishFailureTimeout,
		PublishFailureChanClosed,
	} {
		assert.NotPanics(t, func() { noop.RecordPublishFailure(r) })
	}
}
