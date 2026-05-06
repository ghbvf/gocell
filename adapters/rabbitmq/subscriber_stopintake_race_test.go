package rabbitmq

// TestStopIntake_NoAddAfterWaitRace covers the regression where StopIntake
// Phase 2 invoked r.localWg.Wait() concurrently with drainRemaining's
// run.registerDelivery() (which calls r.localWg.Add(1)). When the WaitGroup
// counter drops to 0 between Add and Wait, the runtime panics with
//
//	sync: WaitGroup misuse: Add called concurrently with Wait
//
// The fix: Phase 2 polls r.inflightCount() (atomic.Int64) instead of calling
// localWg.Wait, so no Wait ever races with the concurrent Add. These tests
// drive a delivery into drainRemaining AFTER StopIntake has fired, which is
// the precondition the previous suite (TestStopIntake_*) never exercised
// because all deliveries were pre-loaded before Subscribe started.

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

// TestStopIntake_NoAddAfterWaitRace_PostCancelDeliveryArrival drives a delivery
// into drainRemaining AFTER StopIntake has dispatched basic.cancel. Without
// the polling-based Phase 2, this combination triggers an Add-after-Wait
// race in localWg.
func TestStopIntake_NoAddAfterWaitRace_PostCancelDeliveryArrival(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 4)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	var handlerCount atomic.Int64
	released := make(chan struct{})
	defer func() {
		select {
		case <-released:
		default:
			close(released)
		}
	}()

	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-released
		handlerCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:                "addafterwait-queue",
		DLXExchange:              "addafterwait.dlx",
		StopIntakePerCallTimeout: testtime.D2s,
		StopIntakeDrainTimeout:   testtime.D5s,
		Clock:                    clock.Real(),
	})

	// Push the first delivery so consumeLoop dispatches it and we have a
	// real inflight handler waiting on `released`. localWg counter == 1
	// when StopIntake fires.
	first := makeDeliveryBody(t, outbox.Entry{
		ID:        "addafterwait-1",
		EventType: "addafterwait",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: first}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "addafterwait.topic"}, handler)
	}()

	// Wait until the first handler is actually running (counter == 1).
	require.Eventually(t, func() bool {
		sub.runsMu.Lock()
		defer sub.runsMu.Unlock()
		for r := range sub.runs {
			if r.inflightCount() == 1 {
				return true
			}
		}
		return false
	}, testtime.D3s, testtime.FastPoll, "first handler must be running before StopIntake fires")

	// Fire StopIntake in a goroutine. Phase 2 begins polling inflightCount().
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sub.StopIntake(context.Background())
	}()

	// Wait until basic.cancel has been issued — this is the moment Phase 2's
	// polling loop is active and any new delivery arriving on the channel
	// will be picked up by drainRemaining.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.cancelCalled
	}, testtime.D3s, testtime.D10ms, "StopIntake must dispatch basic.cancel")

	// Push the SECOND delivery AFTER StopIntake has entered Phase 2. This is
	// the precondition that triggers the Add-after-Wait panic with the old
	// implementation: drainRemaining receives it, calls registerDelivery
	// (= localWg.Add(1)), while Phase 2 is concurrently in localWg.Wait.
	second := makeDeliveryBody(t, outbox.Entry{
		ID:        "addafterwait-2",
		EventType: "addafterwait",
		Payload:   []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 2, Body: second}

	// Release both handlers so they complete.
	close(released)

	// StopIntake must return without panicking once both handlers settle.
	select {
	case err := <-stopDone:
		require.NoError(t, err, "StopIntake must return nil after handlers complete")
	case <-time.After(testtime.D5s):
		t.Fatal("StopIntake did not complete within budget")
	}

	assert.Equal(t, int64(2), handlerCount.Load(),
		"both deliveries (pre-cancel and post-cancel) must be processed")

	// Close the deliveries chan so drainRemaining exits and Subscribe returns.
	close(ch.consumeDeliveries)
	select {
	case <-subDone:
	case <-time.After(testtime.D3s):
		t.Fatal("Subscribe did not exit after deliveries closed")
	}
}

// TestStopIntake_NoAddAfterWaitRace_Stress runs the post-cancel delivery
// arrival scenario 30 times to amplify the Add-after-Wait race window. With
// the old localWg.Wait-based Phase 2, this typically panics within the first
// few iterations under -race. The polling-based fix must complete all
// iterations cleanly.
func TestStopIntake_NoAddAfterWaitRace_Stress(t *testing.T) {
	const iterations = 30

	for i := range iterations {
		t.Run(fmt.Sprintf("iter-%d", i), func(t *testing.T) {
			runPostCancelArrivalScenario(t)
		})
	}
}

// runPostCancelArrivalScenario is the minimal body of the Add-after-Wait
// repro, factored out so the stress test can exercise it 30 times without
// duplicating setup. Each iteration uses fresh Subscriber/Channel instances.
func runPostCancelArrivalScenario(t *testing.T) {
	t.Helper()

	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.consumeDeliveries = make(chan amqp.Delivery, 4)
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	var handlerCount atomic.Int64
	released := make(chan struct{})

	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		<-released
		handlerCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:                "addafterwait-stress",
		DLXExchange:              "addafterwait.stress.dlx",
		StopIntakePerCallTimeout: testtime.D2s,
		StopIntakeDrainTimeout:   testtime.D5s,
		Clock:                    clock.Real(),
	})

	body1 := makeDeliveryBody(t, outbox.Entry{
		ID: "stress-1", EventType: "stress", Payload: []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: body1}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(subCtx, outbox.Subscription{Topic: "stress.topic"}, handler)
	}()

	require.Eventually(t, func() bool {
		sub.runsMu.Lock()
		defer sub.runsMu.Unlock()
		for r := range sub.runs {
			if r.inflightCount() == 1 {
				return true
			}
		}
		return false
	}, testtime.D3s, testtime.FastPoll, "first handler running")

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- sub.StopIntake(context.Background())
	}()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.cancelCalled
	}, testtime.D3s, testtime.D10ms, "basic.cancel issued")

	body2 := makeDeliveryBody(t, outbox.Entry{
		ID: "stress-2", EventType: "stress", Payload: []byte(`{}`),
	})
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 2, Body: body2}

	close(released)

	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(testtime.D5s):
		t.Fatal("StopIntake hung")
	}

	assert.Equal(t, int64(2), handlerCount.Load())

	close(ch.consumeDeliveries)
	select {
	case <-subDone:
	case <-time.After(testtime.D3s):
		t.Fatal("Subscribe did not exit")
	}
}
