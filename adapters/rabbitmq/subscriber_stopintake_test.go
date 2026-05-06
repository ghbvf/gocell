package rabbitmq

// TestStopIntake_DrainSurvivesParentCtxCancel, TestStopIntake_WaitsForInflightAck,
// TestStopIntake_DrainTimeoutReturnsCloseTimeout, TestStopIntakeDrainTimeout_DefaultsTo30s
//
// These tests verify the Batch A (PR-V1-RMQ-LIFECYCLE-HARDEN) hardening of
// StopIntake's inflight-wait and drainRemaining's detached-context invariants.
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN Batch A
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/subscriber.go — drain after cancel

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestStopIntake_DrainSurvivesParentCtxCancel verifies that drain processes
// all prefetched deliveries even after the parent Subscribe ctx is canceled.
//
// Scenario: 3 deliveries are pre-loaded. StopIntake is called. The parent ctx
// is then canceled. All 3 deliveries must still be handled (drain runs on a
// detached context). Once the deliveries channel is closed (broker cancel-ack),
// Subscribe exits.
func TestStopIntake_DrainSurvivesParentCtxCancel(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 3)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	const numDeliveries = 3
	var handlerCount atomic.Int64

	// Handler blocks until released; we release after parent ctx cancel to prove
	// drain survives the ctx cancellation.
	released := make(chan struct{})
	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-released
		handlerCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:   "drain-detached-queue",
		DLXExchange: "drain-detached.dlx",
		Clock:       clock.Real(),
	})

	// Pre-load deliveries before Subscribe starts.
	for i := range numDeliveries {
		body := makeDeliveryBody(t, outbox.Entry{
			ID:        fmt.Sprintf("drain-detached-%d", i),
			EventType: "drain.detached",
			Payload:   []byte(`{}`),
		})
		ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: uint64(i + 1), Body: body}
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "drain.detached.topic"}, handler)
	}()

	// Wait until handlers are running (stuck on <-released).
	require.Eventually(t, func() bool {
		sub.runsMu.Lock()
		defer sub.runsMu.Unlock()
		if len(sub.runs) == 0 {
			return false
		}
		for r := range sub.runs {
			if r.inflightCount() == int64(numDeliveries) {
				return true
			}
		}
		return false
	}, testtime.D3s, testtime.FastPoll, "all %d handler goroutines must be running", numDeliveries)

	// StopIntake → closes stopIntakeCh → consumeLoop enters drainRemaining.
	stopCtx := context.Background()
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sub.StopIntake(stopCtx)
	}()

	// Wait until StopIntake has issued basic.cancel (confirming stopIntakeCh is
	// closed and consumeLoop has entered drainRemaining). Only then cancel the
	// parent Subscribe ctx — this ensures drainRemaining's priority-select has
	// already fired, and the subsequent subCancel tests the detached-ctx invariant.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.cancelCalled
	}, testtime.D2s, testtime.D10ms, "StopIntake must issue basic.cancel before ctx cancel")

	// Cancel the parent Subscribe ctx AFTER StopIntake entered drain mode.
	// This must NOT abort drain — drainRemaining uses context.WithoutCancel.
	subCancel()

	// Release handlers so they complete.
	close(released)

	// StopIntake must complete (waits for inflight handlers via localWg.Wait).
	select {
	case err := <-stopDone:
		require.NoError(t, err, "StopIntake must return nil after inflight handlers complete")
	case <-time.After(testtime.D5s):
		t.Fatal("StopIntake did not complete within 5s after handlers released")
	}

	// All deliveries must have been processed.
	assert.Equal(t, int64(numDeliveries), handlerCount.Load(),
		"all prefetched deliveries must be processed despite parent ctx cancel")

	// Close deliveries channel to let drainRemaining exit (simulates broker cancel-ack).
	close(ch.consumeDeliveries)

	select {
	case <-subDone:
	case <-time.After(testtime.D3s):
		t.Fatal("Subscribe did not exit after deliveries channel closed")
	}
}

// TestStopIntake_WaitsForInflightAck verifies that StopIntake blocks until all
// in-flight processDelivery goroutines have returned, using an atomic.Int64
// counter to observe goroutine progress.
//
// Scenario: handler is controllably blocked. StopIntake is called. We assert
// StopIntake does NOT return while the handler is still running. Once released,
// StopIntake returns nil (all inflight settled).
func TestStopIntake_WaitsForInflightAck(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	var inflight atomic.Int64
	released := make(chan struct{})

	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		inflight.Add(1)
		defer inflight.Add(-1)
		<-released
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:   "inflight-wait-queue",
		DLXExchange: "inflight-wait.dlx",
		Clock:       clock.Real(),
	})

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "inflight-wait-1",
		EventType: "inflight.wait",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "inflight.wait.topic"}, handler)
	}()

	// Wait until handler is actually running (inflight == 1).
	require.Eventually(t, func() bool {
		return inflight.Load() == 1
	}, testtime.D3s, testtime.FastPoll, "handler must be running")

	// StopIntake in a goroutine — it must block until handler returns.
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sub.StopIntake(context.Background())
	}()

	// StopIntake must NOT return while handler is still running.
	// Use a short poll window to verify the block.
	select {
	case <-stopDone:
		t.Fatal("StopIntake returned while handler goroutine was still inflight")
	case <-time.After(testtime.D100ms):
		// Expected: StopIntake is blocking on inflight.
	}

	// handler is still running.
	assert.Equal(t, int64(1), inflight.Load(), "handler must still be running")

	// Release the handler.
	close(released)

	// StopIntake must now complete.
	require.Eventually(t, func() bool {
		select {
		case err := <-stopDone:
			assert.NoError(t, err, "StopIntake must return nil after inflight handler completes")
			return true
		default:
			return false
		}
	}, testtime.D3s, testtime.FastPoll, "StopIntake must complete after handler released")

	// Drain exits and Subscribe returns.
	close(ch.consumeDeliveries)
	select {
	case <-subDone:
	case <-time.After(testtime.D3s):
		t.Fatal("Subscribe did not exit after deliveries channel closed")
	}
}

// TestStopIntake_DrainTimeoutReturnsCloseTimeout verifies that when the inflight
// handler never completes and the drain budget expires, StopIntake returns
// ErrAdapterAMQPCloseTimeout (not hanging indefinitely).
//
// Uses testOnlyDrainDeadlineOverride + StopIntakeDrainTimeout to inject a short
// budget. The handler blocks forever (blocked on neverRelease).
func TestStopIntake_DrainTimeoutReturnsCloseTimeout(t *testing.T) {
	// Inject a short drain deadline for drainRemaining (timer in drainRemaining).
	prev := testOnlyDrainDeadlineOverride
	testOnlyDrainDeadlineOverride = 100 * time.Millisecond
	t.Cleanup(func() { testOnlyDrainDeadlineOverride = prev })

	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 1)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	// Handler blocks forever; neverRelease is closed in test cleanup.
	neverRelease := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-neverRelease:
		default:
			close(neverRelease)
		}
	})

	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-neverRelease
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:              "drain-timeout-queue",
		DLXExchange:            "drain-timeout.dlx",
		StopIntakeDrainTimeout: 120 * time.Millisecond, // slightly longer than drainRemaining timer
		Clock:                  clock.Real(),
	})

	body := makeDeliveryBody(t, outbox.Entry{
		ID:        "drain-timeout-1",
		EventType: "drain.timeout",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	go func() {
		_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: "drain.timeout.topic"}, handler)
	}()

	// Wait until handler is inflight.
	require.Eventually(t, func() bool {
		sub.runsMu.Lock()
		defer sub.runsMu.Unlock()
		for r := range sub.runs {
			if r.inflightCount() > 0 {
				return true
			}
		}
		return false
	}, testtime.D3s, testtime.FastPoll, "handler must be running")

	// Call StopIntake with a generous outer ctx — the drain budget is short.
	start := time.Now()
	err := sub.StopIntake(context.Background())
	elapsed := time.Since(start)

	// Must return ErrAdapterAMQPCloseTimeout after drain budget exceeded.
	require.Error(t, err, "StopIntake must return error when drain budget exceeded")
	assert.ErrorContains(t, err, string(ErrAdapterAMQPCloseTimeout),
		"StopIntake must return ErrAdapterAMQPCloseTimeout on drain timeout")

	// Must return within a reasonable margin of the drain budget.
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"StopIntake must wait at least StopIntakeDrainTimeout; got %s", elapsed)
	assert.Less(t, elapsed, testtime.D1s,
		"StopIntake must not exceed drain budget substantially; got %s", elapsed)
}

// TestStopIntakeDrainTimeout_DefaultsTo30s verifies that SubscriberConfig with
// StopIntakeDrainTimeout == 0 is set to defaultRMQDrainDeadline (30s) by setDefaults.
func TestStopIntakeDrainTimeout_DefaultsTo30s(t *testing.T) {
	sc := SubscriberConfig{
		PrefetchCount:            10,
		StopIntakePerCallTimeout: 2 * time.Second,
		StopIntakeDrainTimeout:   0, // zero → should become 30s
	}
	sc.setDefaults()

	assert.Equal(t, defaultRMQDrainDeadline, sc.StopIntakeDrainTimeout,
		"StopIntakeDrainTimeout must default to defaultRMQDrainDeadline (30s) when zero")
}
