//go:build integration

package rabbitmq

// Wave 2 integration coverage for PR-V1-RMQ-LIFECYCLE-HARDEN Batch A:
// confirms the StopIntake → drain → wait-inflight contract end-to-end against
// a real RabbitMQ broker. Unit tests in subscriber_stopintake_test.go already
// lock the structural invariants; this file proves the broker observes the
// expected ack pattern (unacked == 0 after StopIntake returns).
//
// ref: docs/plans/202605011500-029-master-roadmap.md B12 PR-V1-RMQ-LIFECYCLE-HARDEN

import (
	"context"
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

// stopIntakeIntegrationHandlerDelay paces each handler invocation so several
// deliveries are simultaneously in the prefetch buffer or in-flight when
// StopIntake fires. Longer than testtime.MediumPoll so a single tick of the
// scheduler can park multiple goroutines on it; short enough that the whole
// test finishes within the integration budget.
const stopIntakeIntegrationHandlerDelay = 60 * time.Millisecond

// TestIntegration_StopIntakeDrainsPrefetchedMessages verifies the end-to-end
// drain contract: after StopIntake returns, the broker queue's unacked count
// must be zero and every published message must have been settled by the
// handler (no message is silently dropped).
//
// Scenario:
//  1. Publisher publishes N=5 messages to a fanout exchange.
//  2. Subscriber with PrefetchCount=5 starts processing — handler is slow
//     so several deliveries sit in the prefetch buffer.
//  3. StopIntake fires while at least one handler is in-flight.
//  4. StopIntake must (a) return without error, (b) wait for inflight ack,
//     (c) leave broker queue with messages=0 + consumers=0.
func TestIntegration_StopIntakeDrainsPrefetchedMessages(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	const (
		topic     = "test.stopintake.drain.events"
		queueName = "test.stopintake.drain.queue"
		numMsgs   = 5
	)

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))
	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:     queueName,
		PrefetchCount: numMsgs,
		DLXExchange:   "test.stopintake.drain.dlx",
		Clock:         clock.Real(),
	})

	var handlerStarts atomic.Int32
	var handlerCompletes atomic.Int32
	firstHandlerStarted := make(chan struct{})

	handler := entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		if handlerStarts.Add(1) == 1 {
			close(firstHandlerStarted)
		}
		// Slow handler — keeps multiple deliveries inflight when StopIntake fires.
		time.Sleep(stopIntakeIntegrationHandlerDelay) //archtest:allow:test-sleep handler pacing for inflight overlap; no broker-side sync hook
		handlerCompletes.Add(1)
		return outbox.Ack()
	})

	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{
			Topic:         topic,
			ConsumerGroup: "stopintake-drain",
		}, handler)
	}()

	// Wait until subscriber is ready (queue declared, consumer attached).
	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	// Publish all messages.
	for i := range numMsgs {
		entry := outbox.Entry{
			ID:        "stopintake-drain-evt-" + intToASCII(i),
			EventType: "test.stopintake.drain",
			Payload:   []byte(`{"i":` + intToASCII(i) + `}`),
			CreatedAt: time.Now().UTC(),
		}
		payload, err := outbox.MarshalEnvelope(entry)
		require.NoError(t, err, "marshal envelope")
		require.NoError(t, pub.Publish(subCtx, topic, payload), "publish")
	}

	// Wait until at least one handler has started — guarantees prefetch is
	// non-empty and StopIntake exercises the drain path.
	select {
	case <-firstHandlerStarted:
	case <-time.After(testtime.D5s):
		t.Fatal("no handler started within 5s — broker delivery did not happen")
	}

	// Issue StopIntake with a generous outer ctx; drain budget tracks
	// SubscriberConfig.StopIntakeDrainTimeout (default 30s).
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer stopCancel()
	require.NoError(t, sub.StopIntake(stopCtx), "StopIntake should drain prefetched messages without error")

	// All N handlers must have completed.
	assert.Equal(t, int32(numMsgs), handlerCompletes.Load(),
		"every published message must be handled before StopIntake returns; got %d / %d",
		handlerCompletes.Load(), numMsgs)

	// Broker-side: queue must show 0 messages and 0 consumers (basic.cancel
	// observed; nothing left unacked). Inspect via a fresh channel.
	inspector, releaseInspector := acquireQueueInspector(t, conn)
	defer releaseInspector()
	q, err := inspector.QueueInspect(queueName)
	require.NoError(t, err, "QueueInspect should succeed after drain")
	assert.Equal(t, 0, q.Messages,
		"queue must have zero unacked messages after StopIntake; got %d", q.Messages)
	assert.Equal(t, 0, q.Consumers,
		"queue must have zero consumers after basic.cancel; got %d", q.Consumers)

	// Tear down subscriber.
	subCancel()
	_ = sub.Close(context.Background())
}

// acquireQueueInspector returns a queueInspector backed by a fresh AMQP channel
// and a release helper to put the channel back. The helper is used by drain
// integration assertions to read queue depth without polluting the subscriber's
// channel pool.
func acquireQueueInspector(t *testing.T, conn *Connection) (queueInspector, func()) {
	t.Helper()
	ch, err := conn.AcquireChannel()
	require.NoError(t, err, "acquire inspection channel")
	inspector, ok := ch.(queueInspector)
	require.True(t, ok, "AMQPChannel must support QueueInspect in integration tests")
	return inspector, func() { conn.ReleaseChannel(ch) }
}

// intToASCII renders a small integer (0..9) without importing strconv into
// integration tests for a one-line transform.
func intToASCII(i int) string {
	return string(rune('0' + i))
}

// Compile-time interface assertion: queueInspector remains the integration-
// test-only abstraction defined in integration_test.go.
var _ queueInspector = (*amqp.Channel)(nil)
