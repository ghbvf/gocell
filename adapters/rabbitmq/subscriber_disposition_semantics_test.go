//go:build integration

package rabbitmq

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestSubscriber_DispositionBrokerSemantics asserts broker-side queue
// state for each HandleResult.Disposition value. Coverage gap closed:
// the conformance harness (testDispositionAck/Requeue/Reject) only
// asserts handler invocation counts; this test adds the missing
// broker-state assertions (DLX enqueue / redelivery / ack settled).
//
// Replaces the previous TestIntegration_DLXBrokerNative which only
// covered the Reject branch. All three branches now share one fixture.
//
// ref: rabbitmq/amqp091-go integration_test.go TestRabbitMQQueueNackMultipleRequeue (requeue)
// ref: ThreeDotsLabs/watermill-amqp pubsub/tests — confirms NO open-source
// framework asserts DLX-queue arrival from conformance tests; GoCell exceeds.
func TestSubscriber_DispositionBrokerSemantics(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))
	ctx := context.Background()

	t.Run("Ack/NoDLXNoRedeliver", func(t *testing.T) {
		const (
			topic       = "test.disposition.ack"
			mainQueue   = "test.disposition.ack.main"
			dlxExchange = "test.disposition.ack.dlx"
			dlxQueue    = "test.disposition.ack.dlq"
		)

		// --- Set up DLX infrastructure via raw AMQP channel ---
		rawCh, err := conn.AcquireChannel()
		require.NoError(t, err)

		err = rawCh.ExchangeDeclare(dlxExchange, "direct", true, false, false, false, nil)
		require.NoError(t, err, "declare DLX exchange")

		_, err = rawCh.QueueDeclare(dlxQueue, true, false, false, false, nil)
		require.NoError(t, err, "declare DLQ queue")

		err = rawCh.QueueBind(dlxQueue, "", dlxExchange, false, nil)
		require.NoError(t, err, "bind DLQ to DLX exchange")

		conn.ReleaseChannel(rawCh)

		// --- Start subscriber ---
		sub := NewSubscriber(conn, SubscriberConfig{
			QueueName:     mainQueue,
			PrefetchCount: 1,
			DLXExchange:   dlxExchange,
			Clock:         clock.Real(),
		})

		subCtx, subCancel := context.WithTimeout(ctx, testtime.CtxLong)
		t.Cleanup(subCancel)
		t.Cleanup(func() { _ = sub.Close(context.Background()) })

		var callCount atomic.Int32
		handlerFired := make(chan struct{}, 1)

		subErrCh := make(chan error, 1)
		go func() {
			subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "test-disposition-ack"}, entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				callCount.Add(1)
				select {
				case handlerFired <- struct{}{}:
				default:
				}
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			}))
		}()

		waitForSubscriberReady(t, conn, mainQueue, subErrCh, testtime.EventuallyLong)

		// --- Publish one message ---
		entry := outbox.Entry{
			ID:        "evt-ack-001",
			EventType: topic,
			Payload:   []byte(`{"disposition":"ack"}`),
			CreatedAt: time.Now().UTC(),
		}
		payload, err := outbox.MarshalEnvelope(entry)
		require.NoError(t, err)

		err = pub.Publish(ctx, topic, payload)
		require.NoError(t, err, "publish should succeed")

		// Wait for handler to fire once.
		select {
		case <-handlerFired:
			// Handler was called and returned DispositionAck.
		case <-time.After(testtime.SelectAsyncSettle):
			t.Fatal("timed out waiting for handler to be called")
		}

		// --- Assert: DLX queue must remain empty (no rejected messages routed) ---
		// Use QueueInspect polling over a window instead of time.Sleep+raw-consume;
		// require.Never fails immediately if Messages > 0, proving no DLX routing occurred.
		dlxCh, err := conn.AcquireChannel()
		require.NoError(t, err)
		defer conn.ReleaseChannel(dlxCh)

		dlxInspector, ok := dlxCh.(queueInspector)
		require.True(t, ok, "AMQPChannel must support QueueInspect in integration tests")

		require.Never(t, func() bool {
			q, qErr := dlxInspector.QueueInspect(dlxQueue)
			return qErr == nil && q.Messages > 0
		}, testtime.D500ms, testtime.D50ms,
			"DLX queue must remain empty for Ack disposition (no rejected messages routed)")

		// --- Assert: main queue Messages == 0 after ack settled ---
		inspCh, err := conn.AcquireChannel()
		require.NoError(t, err)
		inspector, ok := inspCh.(queueInspector)
		require.True(t, ok, "AMQPChannel must support QueueInspect in integration tests")
		q, inspErr := inspector.QueueInspect(mainQueue)
		conn.ReleaseChannel(inspCh)
		require.NoError(t, inspErr)
		assert.Equal(t, 0, q.Messages, "main queue Messages must be 0 after ack settled")

		assert.Equal(t, int32(1), callCount.Load(), "handler must be called exactly 1 time")

		subCancel()
		_ = sub.Close(context.Background())
	})

	t.Run("Requeue/BrokerRedelivers", func(t *testing.T) {
		const (
			topic       = "test.disposition.requeue"
			mainQueue   = "test.disposition.requeue.main"
			dlxExchange = "test.disposition.requeue.dlx"
			dlxQueue    = "test.disposition.requeue.dlq"
		)

		// --- Set up DLX infrastructure ---
		rawCh, err := conn.AcquireChannel()
		require.NoError(t, err)

		err = rawCh.ExchangeDeclare(dlxExchange, "direct", true, false, false, false, nil)
		require.NoError(t, err, "declare DLX exchange")

		_, err = rawCh.QueueDeclare(dlxQueue, true, false, false, false, nil)
		require.NoError(t, err, "declare DLQ queue")

		err = rawCh.QueueBind(dlxQueue, "", dlxExchange, false, nil)
		require.NoError(t, err, "bind DLQ to DLX exchange")

		conn.ReleaseChannel(rawCh)

		// --- Start subscriber: Requeue first call, Ack second call ---
		sub := NewSubscriber(conn, SubscriberConfig{
			QueueName:     mainQueue,
			PrefetchCount: 1,
			DLXExchange:   dlxExchange,
			Clock:         clock.Real(),
		})

		subCtx, subCancel := context.WithTimeout(ctx, testtime.CtxLong)
		t.Cleanup(subCancel)
		t.Cleanup(func() { _ = sub.Close(context.Background()) })

		var callCount atomic.Int32

		subErrCh := make(chan error, 1)
		go func() {
			subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "test-disposition-requeue"}, entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				n := callCount.Add(1)
				if n == 1 {
					// First call: return Requeue so broker redelivers.
					return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: assert.AnError}
				}
				// Second (and subsequent) calls: Ack.
				return outbox.HandleResult{Disposition: outbox.DispositionAck}
			}))
		}()

		waitForSubscriberReady(t, conn, mainQueue, subErrCh, testtime.EventuallyLong)

		// --- Publish one message ---
		entry := outbox.Entry{
			ID:        "evt-requeue-001",
			EventType: topic,
			Payload:   []byte(`{"disposition":"requeue"}`),
			CreatedAt: time.Now().UTC(),
		}
		payload, err := outbox.MarshalEnvelope(entry)
		require.NoError(t, err)

		err = pub.Publish(ctx, topic, payload)
		require.NoError(t, err, "publish should succeed")

		// --- Assert: handler called >= 2 times (broker redelivered) ---
		require.Eventually(t, func() bool {
			return callCount.Load() >= 2
		}, testtime.SelectAsyncSettle, testtime.SlowPoll,
			"handler must be called at least 2 times to confirm broker redelivery")

		// --- Assert: DLX queue must remain empty (Requeue+Ack cycle, no DLX routing) ---
		// require.Never proves no message was routed to DLX during the observation window;
		// this is stronger than the prior raw-consume drain which could miss a delayed enqueue.
		dlxCh, err := conn.AcquireChannel()
		require.NoError(t, err)
		defer conn.ReleaseChannel(dlxCh)

		dlxInspector, ok := dlxCh.(queueInspector)
		require.True(t, ok, "AMQPChannel must support QueueInspect in integration tests")

		require.Never(t, func() bool {
			q, qErr := dlxInspector.QueueInspect(dlxQueue)
			return qErr == nil && q.Messages > 0
		}, testtime.D500ms, testtime.D50ms,
			"DLX queue must remain empty for Requeue+Ack cycle (no rejected messages routed)")

		// --- Terminal-state convergence (mirrors Ack subtest assertion strength) ---
		// (a) main queue must drain to Messages=0 after the Ack on the second call
		//     settles the broker — proves the redelivery cycle terminated cleanly,
		//     not stuck in Requeue→Requeue→…
		mainCh, err := conn.AcquireChannel()
		require.NoError(t, err)
		defer conn.ReleaseChannel(mainCh)

		mainInspector, ok := mainCh.(queueInspector)
		require.True(t, ok, "AMQPChannel must support QueueInspect in integration tests")

		require.Eventually(t, func() bool {
			q, qErr := mainInspector.QueueInspect(mainQueue)
			return qErr == nil && q.Messages == 0
		}, testtime.D2s, testtime.D50ms,
			"main queue must drain to Messages=0 after Requeue+Ack cycle terminates")

		// (b) callCount must remain stable — no continued redelivery loop after the
		//     Ack on call #2 settled. Snapshot AFTER (a) succeeds (queue drained)
		//     so any post-drain redelivery (which would re-grow the count) trips Never.
		settledCount := callCount.Load()
		require.Never(t, func() bool {
			return callCount.Load() > settledCount
		}, testtime.D300ms, testtime.D50ms,
			"callCount must remain stable after queue drained (no post-Ack redelivery loop)")

		subCancel()
		_ = sub.Close(context.Background())
	})

	t.Run("Reject/RoutesToDLX", func(t *testing.T) {
		const (
			topic       = "test.disposition.reject"
			mainQueue   = "test.disposition.reject.main"
			dlxExchange = "test.disposition.reject.dlx"
			dlxQueue    = "test.disposition.reject.dlq"
		)

		// --- Set up DLX infrastructure via raw AMQP channel ---
		rawCh, err := conn.AcquireChannel()
		require.NoError(t, err)

		err = rawCh.ExchangeDeclare(dlxExchange, "direct", true, false, false, false, nil)
		require.NoError(t, err, "declare DLX exchange")

		_, err = rawCh.QueueDeclare(dlxQueue, true, false, false, false, nil)
		require.NoError(t, err, "declare DLQ queue")

		err = rawCh.QueueBind(dlxQueue, "", dlxExchange, false, nil)
		require.NoError(t, err, "bind DLQ to DLX exchange")

		conn.ReleaseChannel(rawCh)

		// --- Start subscriber: handler always returns DispositionReject ---
		sub := NewSubscriber(conn, SubscriberConfig{
			QueueName:     mainQueue,
			PrefetchCount: 1,
			DLXExchange:   dlxExchange,
			Clock:         clock.Real(),
		})

		subCtx, subCancel := context.WithTimeout(ctx, testtime.D20s)
		t.Cleanup(subCancel)
		t.Cleanup(func() { _ = sub.Close(context.Background()) })

		handlerCalled := make(chan struct{}, 1)
		subErrCh := make(chan error, 1)
		go func() {
			subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "test-disposition-reject"}, entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				select {
				case handlerCalled <- struct{}{}:
				default:
				}
				// Permanent rejection — broker should route to DLX.
				return outbox.HandleResult{
					Disposition: outbox.DispositionReject,
					Err:         outbox.NewPermanentError(assert.AnError),
				}
			}))
		}()

		waitForSubscriberReady(t, conn, mainQueue, subErrCh, testtime.EventuallyLong)

		// --- Publish a message ---
		entry := outbox.Entry{
			ID:        "evt-reject-001",
			EventType: topic,
			Payload:   []byte(`{"disposition":"reject"}`),
			CreatedAt: time.Now().UTC(),
		}
		payload, err := outbox.MarshalEnvelope(entry)
		require.NoError(t, err)

		err = pub.Publish(ctx, topic, payload)
		require.NoError(t, err, "publish should succeed")

		// Wait for handler to be called (message consumed → Reject).
		select {
		case <-handlerCalled:
			// Handler was called and returned DispositionReject.
		case <-time.After(testtime.SelectAsyncSettle):
			t.Fatal("timed out waiting for handler to be called")
		}

		// --- Consume from DLX queue via raw AMQP, assert entry arrives ---
		// Single consumer setup, then poll delivery channel.
		dlxCh, err := conn.AcquireChannel()
		require.NoError(t, err)
		defer conn.ReleaseChannel(dlxCh)

		dlxMsgs, err := dlxCh.Consume(dlxQueue, "reject-dlx-consumer", true, false, false, false, nil)
		require.NoError(t, err, "consume from DLQ")

		var dlEntry outbox.Entry
		require.Eventually(t, func() bool {
			select {
			case msg := <-dlxMsgs:
				decoded, decodeErr := outbox.UnmarshalEnvelope("", msg.Body)
				if decodeErr != nil {
					return false
				}
				dlEntry = decoded
				return true
			default:
				return false
			}
		}, testtime.SelectAsyncSettle, testtime.SlowPoll,
			"message should appear in dead-letter queue — DLX routing failed")

		assert.Equal(t, "evt-reject-001", dlEntry.ID, "dead-lettered entry ID must match")
		assert.JSONEq(t, `{"disposition":"reject"}`, string(dlEntry.Payload), "payload must match")
		t.Logf("Reject/RoutesToDLX verified: message %s arrived in dead-letter queue", dlEntry.ID)

		subCancel()
		_ = sub.Close(context.Background())
	})
}
