//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// startRabbitMQWithContainer launches a testcontainers RabbitMQ instance and
// returns the Connection, the container (for Exec/Stop operations), and a
// cleanup function.
func startRabbitMQWithContainer(t *testing.T, config Config) (*Connection, *tcrabbitmq.RabbitMQContainer, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcrabbitmq.Run(ctx, "rabbitmq:3.12-management-alpine")
	require.NoError(t, err, "start rabbitmq container")

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get rabbitmq amqp url")

	config.URL = amqpURL
	if config.ChannelPoolSize == 0 {
		config.ChannelPoolSize = 5
	}
	if config.ConfirmTimeout == 0 {
		config.ConfirmTimeout = 10 * time.Second
	}

	conn, err := NewConnection(config)
	require.NoError(t, err, "create rabbitmq connection")

	cleanup := func() {
		_ = conn.Close()
		_ = container.Terminate(ctx)
	}

	return conn, container, cleanup
}

// startRabbitMQ is a convenience wrapper that discards the container reference.
func startRabbitMQ(t *testing.T) (*Connection, func()) {
	conn, _, cleanup := startRabbitMQWithContainer(t, Config{
		ReconnectMaxBackoff: 5 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
	})
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
	assert.Equal(t, StateConnected, conn.ConnectionStatus())
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

// TestIntegration_ConsumerBaseRetry verifies the full end-to-end path:
//
//	publish → subscriber consume → ConsumerBase retry exhaustion → broker Nack
//	→ DLX routing → dead-letter queue receives the message
//
// Unlike the previous version (which invoked the wrapped handler directly),
// this test publishes through the broker and verifies that ConsumerBase retry
// logic works correctly when integrated with the real Subscriber.
func TestIntegration_ConsumerBaseRetry(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	ctx := context.Background()
	pub := NewPublisher(conn)

	const (
		topic       = "test.retry.e2e"
		dlxExchange = "test.retry.e2e.dlx"
		dlxQueue    = "test.retry.e2e.dlq"
		mainQueue   = "test.retry.e2e.main"
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

	// --- Create ConsumerBase with short retry ---
	cb := NewConsumerBase(
		&noopClaimer{},
		ConsumerBaseConfig{
			ConsumerGroup:  "test-retry-e2e",
			RetryCount:     2,
			RetryBaseDelay: 50 * time.Millisecond,
			IdempotencyTTL: time.Hour,
		},
	)

	// --- Start main subscriber with ConsumerBase-wrapped handler ---
	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       mainQueue,
		PrefetchCount:   1,
		DLXExchange:     dlxExchange,
		ShutdownTimeout: 5 * time.Second,
	})

	var callCount atomic.Int32
	subCtx, subCancel := context.WithTimeout(ctx, 30*time.Second)
	defer subCancel()

	wrappedHandler := cb.Wrap(topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: assert.AnError}
	})

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, topic, wrappedHandler, "test-retry-e2e")
	}()

	waitForSubscriberReady(t, conn, mainQueue, subErrCh, 5*time.Second)

	// --- Publish a message ---
	entry := outbox.Entry{
		ID:        "evt-retry-e2e-001",
		EventType: "test.retry.transient",
		Payload:   []byte(`{"retry":"e2e"}`),
		CreatedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	err = pub.Publish(ctx, topic, payload)
	require.NoError(t, err, "publish should succeed")

	// --- Verify: message appears in DLQ after retry exhaustion ---
	// Set up a single consumer ONCE, then poll its delivery channel inside
	// Eventually. This avoids creating multiple competing consumers on
	// different channels (R2-P1-A review fix).
	dlxCh, err := conn.AcquireChannel()
	require.NoError(t, err)
	defer conn.ReleaseChannel(dlxCh)

	dlxMsgs, err := dlxCh.Consume(dlxQueue, "retry-dlx-consumer", true, false, false, false, nil)
	require.NoError(t, err, "consume from DLQ")

	var dlEntry outbox.Entry
	require.Eventually(t, func() bool {
		select {
		case msg := <-dlxMsgs:
			return json.Unmarshal(msg.Body, &dlEntry) == nil
		default:
			return false
		}
	}, 15*time.Second, 200*time.Millisecond,
		"message should appear in DLQ after retry exhaustion — handler called %d times", callCount.Load())

	assert.Equal(t, "evt-retry-e2e-001", dlEntry.ID, "dead-lettered entry ID should match")
	assert.JSONEq(t, `{"retry":"e2e"}`, string(dlEntry.Payload))
	t.Logf("ConsumerBase retry e2e verified: message %s routed to DLQ after %d handler calls",
		dlEntry.ID, callCount.Load())

	// Handler should have been called RetryCount times.
	assert.GreaterOrEqual(t, callCount.Load(), int32(2),
		"handler should be called at least RetryCount times before rejection")

	subCancel()
	_ = sub.Close()
}

// TestIntegration_ConnectionRecovery verifies that the Connection automatically
// reconnects after the broker forcibly closes all client connections.
//
// Uses rabbitmqctl close_all_connections (not container stop/start) to avoid
// port remapping issues — the broker stays on the same address.
func TestIntegration_ConnectionRecovery(t *testing.T) {
	conn, container, cleanup := startRabbitMQWithContainer(t, Config{
		ReconnectBaseDelay:  200 * time.Millisecond,
		ReconnectMaxBackoff: 2 * time.Second,
	})
	defer cleanup()

	ctx := context.Background()

	// 1. Verify initial healthy state.
	require.NoError(t, conn.Health(), "initial Health should be nil")
	assert.Equal(t, StateConnected, conn.ConnectionStatus())

	// 2. Force-close all connections via rabbitmqctl.
	exitCode, _, err := container.Exec(ctx, []string{
		"rabbitmqctl", "close_all_connections", "integration-test",
	})
	require.NoError(t, err, "rabbitmqctl exec should not error")
	require.Equal(t, 0, exitCode, "rabbitmqctl should exit 0")

	// 3. Health() should return error during reconnect.
	require.Eventually(t, func() bool {
		return conn.Health() != nil
	}, 5*time.Second, 50*time.Millisecond,
		"Health() should report error after broker-forced disconnect")

	// Verify the state is Disconnected (not Terminal).
	status := conn.ConnectionStatus()
	assert.True(t, status == StateDisconnected || status == StateConnecting,
		"state should be Disconnected or Connecting during reconnect, got %s", status)

	// 4. Health() should recover after reconnect succeeds.
	require.Eventually(t, func() bool {
		return conn.Health() == nil
	}, 10*time.Second, 100*time.Millisecond,
		"Health() should recover after successful reconnect")

	assert.Equal(t, StateConnected, conn.ConnectionStatus(),
		"state should be Connected after recovery")

	// 5. Verify connection is usable: acquire and release a channel.
	ch, err := conn.AcquireChannel()
	require.NoError(t, err, "AcquireChannel should succeed after recovery")
	conn.ReleaseChannel(ch)

	// 6. WaitConnected should return immediately.
	waitCtx, waitCancel := context.WithTimeout(ctx, time.Second)
	defer waitCancel()
	require.NoError(t, conn.WaitConnected(waitCtx),
		"WaitConnected should return nil after recovery")
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
	// Single consumer setup, then poll delivery channel (R2-P2 review fix).
	dlxCh, err := conn.AcquireChannel()
	require.NoError(t, err)
	defer conn.ReleaseChannel(dlxCh)

	dlxMsgs, err := dlxCh.Consume(dlxQueue, "dlx-test-consumer", true, false, false, false, nil)
	require.NoError(t, err, "consume from DLQ")

	var dlEntry outbox.Entry
	require.Eventually(t, func() bool {
		select {
		case msg := <-dlxMsgs:
			return json.Unmarshal(msg.Body, &dlEntry) == nil
		default:
			return false
		}
	}, 10*time.Second, 100*time.Millisecond,
		"message should appear in dead-letter queue — DLX routing failed")

	assert.Equal(t, "evt-dlx-e2e-001", dlEntry.ID, "dead-lettered entry ID should match")
	assert.JSONEq(t, `{"dlx":"end-to-end"}`, string(dlEntry.Payload), "payload should match")
	t.Logf("DLX end-to-end verified: message %s arrived in dead-letter queue", dlEntry.ID)

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
