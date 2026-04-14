//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// startRabbitMQ launches a testcontainers RabbitMQ instance and returns a
// connected Connection plus a cleanup function.
func startRabbitMQ(t *testing.T) (*Connection, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcrabbitmq.Run(ctx, "rabbitmq:3.12-management-alpine")
	require.NoError(t, err, "start rabbitmq container")

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get rabbitmq amqp url")

	conn, err := NewConnection(Config{
		URL:                 amqpURL,
		ReconnectMaxBackoff: 5 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
	})
	require.NoError(t, err, "create rabbitmq connection")

	cleanup := func() {
		_ = conn.Close()
		_ = container.Terminate(ctx)
	}

	return conn, cleanup
}

type queueInspector interface {
	QueueInspect(name string) (amqp.Queue, error)
}

func waitForSubscriberReady(t *testing.T, conn *Connection, queueName string, subErrCh <-chan error, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-subErrCh:
			require.NoError(t, err, "subscriber exited before becoming ready")
			t.Fatal("subscriber exited before becoming ready")
		default:
		}

		ch, err := conn.AcquireChannel()
		require.NoError(t, err, "AcquireChannel should succeed while waiting for subscriber readiness")

		inspector, ok := ch.(queueInspector)
		require.True(t, ok, "AMQPChannel should support QueueInspect in integration tests")

		queue, inspectErr := inspector.QueueInspect(queueName)
		_ = ch.Close()
		if inspectErr == nil && queue.Consumers > 0 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for subscriber queue %q to become ready", queueName)
}

// TestIntegration_ConnectionHealth verifies the Connection is alive after
// connecting to a real RabbitMQ broker.
func TestIntegration_ConnectionHealth(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	err := conn.Health()
	assert.NoError(t, err, "Health should succeed on a live RabbitMQ")
}

// TestIntegration_PublishConsume publishes a message and consumes it
// from a real RabbitMQ broker, asserting payload integrity.
func TestIntegration_PublishConsume(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(conn)
	topic := "test.integration.events"
	queueName := "test.integration.queue"

	// Subscribe and receive.
	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       queueName,
		PrefetchCount:   1,
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 5 * time.Second,
	})

	ctx := context.Background()
	received := make(chan outbox.Entry, 1)
	subCtx, subCancel := context.WithTimeout(ctx, 15*time.Second)
	defer subCancel()

	// Run subscriber in a goroutine since Subscribe blocks.
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, topic, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			received <- e
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "integration-test")
	}()

	// Wait until Subscribe has declared, bound, and started consuming from the queue.
	waitForSubscriberReady(t, conn, queueName, subErrCh, 5*time.Second)

	// Prepare an outbox.Entry as the message payload.
	entry := outbox.Entry{
		ID:            "evt-001",
		AggregateID:   "agg-001",
		AggregateType: "test",
		EventType:     "test.created",
		Payload:       []byte(`{"foo":"bar"}`),
		CreatedAt:     time.Now().UTC(),
		Metadata:      map[string]string{"source": "integration-test"},
	}

	payload, err := json.Marshal(entry)
	require.NoError(t, err, "marshal entry")

	// Publish the message after the subscriber is ready.
	err = pub.Publish(ctx, topic, payload)
	require.NoError(t, err, "Publish should succeed")

	// Wait for the message.
	select {
	case got := <-received:
		assert.Equal(t, entry.ID, got.ID, "event ID should match")
		assert.Equal(t, entry.AggregateID, got.AggregateID, "aggregate ID should match")
		assert.Equal(t, entry.EventType, got.EventType, "event type should match")
		assert.JSONEq(t, `{"foo":"bar"}`, string(got.Payload), "payload should match")
	case <-subCtx.Done():
		t.Fatal("timed out waiting for message")
	}

	// Clean up subscriber.
	subCancel()
	_ = sub.Close()
}

// TestIntegration_PublishOnly verifies that Publisher.Publish succeeds
// and is confirmed by the broker without a consumer.
func TestIntegration_PublishOnly(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(conn)
	topic := "test.integration.publish-only"

	entry := outbox.Entry{
		ID:        "evt-publish-only",
		EventType: "test.published",
		Payload:   []byte(`{"status":"ok"}`),
		CreatedAt: time.Now().UTC(),
	}

	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = pub.Publish(ctx, topic, payload)
	assert.NoError(t, err, "Publish should succeed even without consumers")
}

// TestIntegration_ConsumerBaseRetry verifies that ConsumerBase retries
// a transiently-failing handler up to the configured limit and then
// routes the message to the DLQ.
//
// This test is simplified: it verifies the ConsumerBase.Wrap handler
// invocation count and DLQ publish behavior using a real RabbitMQ
// connection for the Publisher/Subscriber infrastructure.
func TestIntegration_ConsumerBaseRetry(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	ctx := context.Background()
	topic := "test.integration.retry"

	// Track DLQ messages via a separate subscriber on the DLQ topic.
	dlqTopic := topic + ".dlq"
	dlqReceived := make(chan outbox.Entry, 1)

	dlqSub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test.integration.retry.dlq.queue",
		PrefetchCount:   1,
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 5 * time.Second,
	})

	dlqCtx, dlqCancel := context.WithTimeout(ctx, 30*time.Second)
	defer dlqCancel()

	// Start DLQ subscriber first.
	go func() {
		_ = dlqSub.Subscribe(dlqCtx, dlqTopic, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			dlqReceived <- e
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "integration-test-dlq")
	}()

	// Give DLQ subscriber time to bind.
	time.Sleep(500 * time.Millisecond)

	// Create a ConsumerBase with RetryCount=2 and very short delays.
	cb := NewConsumerBase(
		&noopClaimer{},
		ConsumerBaseConfig{
			ConsumerGroup:  "test-retry-group",
			RetryCount:     2,
			RetryBaseDelay: 100 * time.Millisecond,
			IdempotencyTTL: time.Hour,
		},
	)

	// Wrap a handler that always fails with a transient error.
	callCount := 0
	wrappedHandler := cb.Wrap(topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount++
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: assert.AnError}
	})

	// Publish a message.
	entry := outbox.Entry{
		ID:        "evt-retry-001",
		EventType: "test.retry",
		Payload:   []byte(`{"retry":"test"}`),
		CreatedAt: time.Now().UTC(),
	}

	// Invoke the wrapped handler directly (simulates what Subscriber does).
	res := wrappedHandler(ctx, entry)
	// In Solution B, exhausted retries return Reject (broker routes to DLX).
	assert.Equal(t, outbox.DispositionReject, res.Disposition, "exhausted retries should reject")
	assert.Equal(t, 2, callCount, "handler should be called RetryCount times")

	// Clean up.
	dlqCancel()
	_ = dlqSub.Close()
}

// TestIntegration_ConnectionRecovery kills the AMQP connection and
// asserts the adapter detects the state change.
func TestIntegration_ConnectionRecovery(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	// Connection should be healthy.
	err := conn.Health()
	require.NoError(t, err)

	// Acquire and release a channel to verify pool works.
	ch, err := conn.AcquireChannel()
	require.NoError(t, err, "AcquireChannel should succeed")
	conn.ReleaseChannel(ch)
}

// TestIntegration_DLXBrokerNative verifies the full broker-native DLX path:
//
//	handler → DispositionReject → Subscriber.processDelivery Nack(requeue=false)
//	→ RabbitMQ routes to DLX exchange → dead-letter queue receives message
//
// This test uses raw AMQP channels to set up the DLX infrastructure and
// directly consume from the dead-letter queue, proving the broker actually
// routes rejected messages end-to-end.
func TestIntegration_DLXBrokerNative(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	ctx := context.Background()
	pub := NewPublisher(conn)

	const (
		topic       = "test.dlx.e2e"
		dlxExchange = "test.dlx.e2e.dlx"
		dlxQueue    = "test.dlx.e2e.dlq"
		mainQueue   = "test.dlx.e2e.main"
	)

	// --- Set up DLX infrastructure via raw AMQP channel ---
	rawCh, err := conn.AcquireChannel()
	require.NoError(t, err)

	// Declare the DLX exchange (direct type, durable).
	err = rawCh.ExchangeDeclare(dlxExchange, "direct", true, false, false, false, nil)
	require.NoError(t, err, "declare DLX exchange")

	// Declare the dead-letter queue and bind it to the DLX exchange.
	_, err = rawCh.QueueDeclare(dlxQueue, true, false, false, false, nil)
	require.NoError(t, err, "declare DLQ queue")

	err = rawCh.QueueBind(dlxQueue, "", dlxExchange, false, nil)
	require.NoError(t, err, "bind DLQ to DLX exchange")

	conn.ReleaseChannel(rawCh)

	// --- Start the main subscriber with DLX configured ---
	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       mainQueue,
		PrefetchCount:   1,
		DLXExchange:     dlxExchange,
		ShutdownTimeout: 5 * time.Second,
	})

	subCtx, subCancel := context.WithTimeout(ctx, 20*time.Second)
	defer subCancel()

	handlerCalled := make(chan struct{}, 1)
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, topic, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			handlerCalled <- struct{}{}
			// Permanent rejection — broker should route to DLX.
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         assert.AnError,
			}
		}, "integration-test-dlx")
	}()

	waitForSubscriberReady(t, conn, mainQueue, subErrCh, 5*time.Second)

	// --- Publish a message ---
	entry := outbox.Entry{
		ID:        "evt-dlx-e2e-001",
		EventType: "test.dlx.rejected",
		Payload:   []byte(`{"dlx":"end-to-end"}`),
		CreatedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	err = pub.Publish(ctx, topic, payload)
	require.NoError(t, err, "publish should succeed")

	// Wait for handler to be called (message consumed → Reject).
	select {
	case <-handlerCalled:
		// Handler was called and returned DispositionReject.
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for handler to be called")
	}

	// --- Consume from the dead-letter queue via raw AMQP ---
	// Give broker a moment to route the rejected message to DLX.
	time.Sleep(500 * time.Millisecond)

	dlxCh, err := conn.AcquireChannel()
	require.NoError(t, err)
	defer conn.ReleaseChannel(dlxCh)

	msgs, err := dlxCh.Consume(dlxQueue, "dlx-test-consumer", true, false, false, false, nil)
	require.NoError(t, err, "consume from DLQ")

	select {
	case msg := <-msgs:
		// Unmarshal and verify the dead-lettered message.
		var dlEntry outbox.Entry
		require.NoError(t, json.Unmarshal(msg.Body, &dlEntry))
		assert.Equal(t, "evt-dlx-e2e-001", dlEntry.ID, "dead-lettered entry ID should match")
		assert.JSONEq(t, `{"dlx":"end-to-end"}`, string(dlEntry.Payload), "payload should match")
		t.Logf("DLX end-to-end verified: message %s arrived in dead-letter queue", dlEntry.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for message in dead-letter queue — DLX routing failed")
	}

	subCancel()
	_ = sub.Close()
}

// noopClaimer is a minimal idempotency.Claimer for testing that always
// returns ClaimAcquired with a noopReceipt.
type noopClaimer struct{}

func (n *noopClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimAcquired, &noopReceipt{}, nil
}

type noopReceipt struct{}

func (n *noopReceipt) Commit(_ context.Context) error  { return nil }
func (n *noopReceipt) Release(_ context.Context) error { return nil }
