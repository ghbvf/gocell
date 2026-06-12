//go:build integration

package rabbitmq

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/tests/testutil/rabbitmqctr"
)

// startRabbitMQDedicatedContainer launches a NEW testcontainers RabbitMQ
// instance used by a single test that must not share broker state with any
// other test. Examples: rabbitmqctl close_all_connections, container.Exec,
// container.Stop/Start — any operation that mutates broker-wide state.
//
// The returned cleanup terminates both the Connection and the container.
// Tests that do not mutate broker state should use startRabbitMQ (shared
// broker) — this helper starts its own container on every call, which is
// ~5-7s more expensive.
func startRabbitMQDedicatedContainer(t *testing.T, config Config) (*Connection, *tcrabbitmq.RabbitMQContainer, func()) {
	t.Helper()

	ctx := context.Background()

	container := rabbitmqctr.StartRabbitMQContainer(t, ctx)

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get dedicated rabbitmq amqp url")

	config.URL = amqpURL
	if config.ChannelPoolSize == 0 {
		config.ChannelPoolSize = 5
	}
	if config.ConfirmTimeout == 0 {
		config.ConfirmTimeout = testtime.SelectAsyncSettle
	}

	conn, err := NewConnection(clock.Real(), config)
	require.NoError(t, err, "create dedicated rabbitmq connection")

	cleanup := func() {
		_ = conn.Close(context.Background())
		if err := container.Terminate(ctx); err != nil {
			t.Logf("rabbitmq: dedicated container terminate failed: %v", err)
		}
	}

	return conn, container, cleanup
}

// startRabbitMQ returns a per-test Connection backed by the package-wide
// shared broker (see testmain_integration_test.go). Only the Connection is
// torn down via the returned cleanup; the container lives until TestMain
// exits.
//
// Contract: callers MUST NOT mutate broker-wide state. That means no
// rabbitmqctl, no container.Exec, no container.Stop/Start, no policy
// changes that outlive the test. All exchange/queue/topic names must be
// unique per test (use `test.<testname>.*` or TestTopic(t)). Tests that
// need broker-level isolation must call startRabbitMQDedicatedContainer.
func startRabbitMQ(t *testing.T) (*Connection, func()) {
	t.Helper()
	url := sharedBrokerURL(t)
	conn, err := NewConnection(clock.Real(), Config{
		URL:                 url,
		ChannelPoolSize:     5,
		ConfirmTimeout:      testtime.SelectAsyncSettle,
		ReconnectMaxBackoff: testtime.EventuallyLong,
		ReconnectBaseDelay:  testtime.D500ms,
	})
	require.NoError(t, err, "create connection against shared rabbitmq broker")
	return conn, func() { _ = conn.Close(context.Background()) }
}

// startRabbitMQBroker returns the package-wide shared broker AMQP URL
// (see sharedBrokerURL in testmain_integration_test.go). The returned
// cleanup is a no-op: the shared container is torn down in TestMain.
//
// Contract: same as startRabbitMQ — callers create their own Connections
// against this URL (typically via newIntegrationConnection for the
// per-subtest pattern used by conformance_test.go) and must not mutate
// broker-wide state. For broker-level isolation, use
// startRabbitMQDedicatedContainer instead.
func startRabbitMQBroker(t *testing.T) (amqpURL string, cleanup func()) {
	t.Helper()
	return sharedBrokerURL(t), func() {}
}

// newIntegrationConnection creates a fresh Connection pointing at the given
// broker URL. The connection is registered for cleanup with t.Cleanup.
// Use this when a per-subtest Connection is needed against a shared container.
func newIntegrationConnection(t *testing.T, amqpURL string) *Connection {
	t.Helper()
	conn, err := NewConnection(clock.Real(), Config{
		URL:                 amqpURL,
		ChannelPoolSize:     5,
		ConfirmTimeout:      testtime.SelectAsyncSettle,
		ReconnectMaxBackoff: testtime.EventuallyLong,
		ReconnectBaseDelay:  testtime.D500ms,
	})
	require.NoError(t, err, "create per-subtest rabbitmq connection")
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
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

		time.Sleep(testtime.MediumPoll) //archtest:allow:test-sleep poll loop waiting for RabbitMQ queue consumer to register; no sync hook
	}

	t.Fatalf("timed out waiting for subscriber queue %q to become ready", queueName)
}

// TestIntegration_ConnectionHealth verifies the Connection is alive after
// connecting to a real RabbitMQ broker.
func TestIntegration_ConnectionHealth(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	err := conn.Health(context.Background())
	assert.NoError(t, err, "Health should succeed on a live RabbitMQ")
	assert.Equal(t, StateConnected, conn.ConnectionStatus().State)
}

// TestIntegration_PublishConsume publishes a message and consumes it
// from a real RabbitMQ broker, asserting payload integrity.
func TestIntegration_PublishConsume(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(clock.Real(), conn)
	topic := "test.integration.events"
	queueName := "test.integration.queue"

	// Subscribe and receive.
	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:     queueName,
		PrefetchCount: 1,
		DLXExchange:   "test.dlx",
	})

	ctx := context.Background()
	received := make(chan outbox.Entry, 1)
	subCtx, subCancel := context.WithTimeout(ctx, testtime.D15s)
	defer subCancel()

	// Run subscriber in a goroutine since Subscribe blocks.
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "integration-test", CellID: "integration-test"}, entryToSubHandler(func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			received <- e
			return outbox.Ack()
		}))
	}()

	// Wait until Subscribe has declared, bound, and started consuming from the queue.
	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	// Prepare an outbox.Entry as the message payload, wrapped in a v1 wire
	// envelope so the subscriber's unmarshalDelivery (fail-closed since P1-14)
	// accepts it.
	entry := mustNewEntry(t, "test.created", []byte(`{"foo":"bar"}`), outbox.WithID("evt-001"), outbox.WithAggregateID("agg-001"), outbox.WithAggregateType("test"), outbox.WithMetadata(map[string]string{"source": "integration-test"}), outbox.WithCreatedAt(time.Now().UTC()))

	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err, "marshal envelope")

	// Publish the message after the subscriber is ready.
	err = pub.Publish(ctx, topic, payload)
	require.NoError(t, err, "Publish should succeed")

	// Wait for the message.
	select {
	case got := <-received:
		assert.Equal(t, entry.ID(), got.ID(), "event ID should match")
		assert.Equal(t, entry.AggregateID(), got.AggregateID(), "aggregate ID should match")
		assert.Equal(t, entry.EventType(), got.EventType(), "event type should match")
		assert.JSONEq(t, `{"foo":"bar"}`, string(got.Payload()), "payload should match")
	case <-subCtx.Done():
		t.Fatal("timed out waiting for message")
	}

	// Clean up subscriber.
	subCancel()
	_ = sub.Close(context.Background())
}

// TestIntegration_PublishOnly verifies that Publisher.Publish succeeds
// and is confirmed by the broker without a consumer.
func TestIntegration_PublishOnly(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(clock.Real(), conn)
	topic := "test.integration.publish-only"

	entry := mustNewEntry(t, "test.published", []byte(`{"status":"ok"}`), outbox.WithID("evt-publish-only"), outbox.WithCreatedAt(time.Now().UTC()))

	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.SelectAsyncSettle)
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
	pub := NewPublisher(clock.Real(), conn)

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
	cb, cbErr := outbox.NewConsumerBase(
		&noopClaimer{},
		outbox.ConsumerBaseConfig{
			RetryCount:     2,
			RetryBaseDelay: testtime.MediumPoll,
			IdempotencyTTL: time.Hour,
		},
		clock.Real(),
	)
	require.NoError(t, cbErr)

	// --- Start main subscriber with ConsumerBase-wrapped handler ---
	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:     mainQueue,
		PrefetchCount: 1,
		DLXExchange:   dlxExchange,
	})

	var callCount atomic.Int32
	subCtx, subCancel := context.WithTimeout(ctx, testtime.CtxLong)
	defer subCancel()

	wrappedHandler := cb.Wrap(outbox.Subscription{Topic: topic, ConsumerGroup: "test-retry-e2e", CellID: "test-retry-e2e"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		return outbox.Requeue(assert.AnError)
	})

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "test-retry-e2e", CellID: "test-retry-e2e"}, wrappedHandler)
	}()

	waitForSubscriberReady(t, conn, mainQueue, subErrCh, testtime.EventuallyLong)

	// --- Publish a message ---
	entry := mustNewEntry(t, "test.retry.transient", []byte(`{"retry":"e2e"}`), outbox.WithID("evt-retry-e2e-001"), outbox.WithCreatedAt(time.Now().UTC()))
	payload, err := outbox.MarshalEnvelope(entry)
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
	testwait.External(t, "amqp-delivery-acked", func() bool {
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
	}, testtime.D15s, testtime.D200ms,
		"message should appear in DLQ after retry exhaustion — handler called %d times", callCount.Load())

	assert.Equal(t, "evt-retry-e2e-001", dlEntry.ID(), "dead-lettered entry ID should match")
	assert.JSONEq(t, `{"retry":"e2e"}`, string(dlEntry.Payload()))
	t.Logf("ConsumerBase retry e2e verified: message %s routed to DLQ after %d handler calls",
		dlEntry.ID(), callCount.Load())

	// Handler should have been called RetryCount times.
	assert.GreaterOrEqual(t, callCount.Load(), int32(2),
		"handler should be called at least RetryCount times before rejection")

	subCancel()
	_ = sub.Close(context.Background())
}

// TestIntegration_WebhookDelaySchedule_RoutesToDLQAfterExhaustion verifies the
// broker-native delayed re-delivery path end to end against a real broker
// (#1458, F3): an always-Requeue handler is redelivered through the TTL+DLX delay
// tiers per the schedule and, once the attempt count exceeds the schedule length,
// is Nack(requeue=false)'d to the queue's real DLX — where this test consumes it
// and verifies the entry survived intact. The shared conformance suite asserts
// the per-attempt delivery count; this test closes the gap that a count-only
// assertion would pass even if the exhausted message were silently dropped
// (DLX binding/routing fault) instead of dead-lettered.
func TestIntegration_WebhookDelaySchedule_RoutesToDLQAfterExhaustion(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	ctx := context.Background()
	pub := NewPublisher(clock.Real(), conn)

	const (
		topic       = "test.webhookdelay.e2e"
		dlxExchange = "test.webhookdelay.e2e.dlx"
		dlxQueue    = "test.webhookdelay.e2e.dlq"
		mainQueue   = "test.webhookdelay.e2e.main"
	)
	// Two short tiers: deliver → delay.0 → delay.1 → exhaust → real DLX. Tiny
	// TTLs keep the test fast while still exercising the real broker TTL→DLX hop
	// (and the F1 publisher-confirm + F6 x-death provenance on the live path).
	schedule := []time.Duration{testtime.D200ms, testtime.D200ms}
	wantHandlerCalls := int32(len(schedule) + 1) // immediate attempt + one per tier

	// --- Set up the real DLX where exhausted messages land ---
	rawCh, err := conn.AcquireChannel()
	require.NoError(t, err)
	require.NoError(t, rawCh.ExchangeDeclare(dlxExchange, "direct", true, false, false, false, nil), "declare DLX exchange")
	_, err = rawCh.QueueDeclare(dlxQueue, true, false, false, false, nil)
	require.NoError(t, err, "declare DLQ queue")
	require.NoError(t, rawCh.QueueBind(dlxQueue, "", dlxExchange, false, nil), "bind DLQ to DLX exchange")
	conn.ReleaseChannel(rawCh)

	// Broker-delay subscriptions run ConsumerBase in single-attempt pass-through,
	// so the transport — not the in-process retry budget — owns the schedule.
	cb, cbErr := outbox.NewConsumerBase(
		&noopClaimer{},
		outbox.ConsumerBaseConfig{RetryCount: 2, RetryBaseDelay: testtime.MediumPoll, IdempotencyTTL: time.Hour},
		clock.Real(),
	)
	require.NoError(t, cbErr)

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:     mainQueue,
		PrefetchCount: 1,
		DLXExchange:   dlxExchange,
	})

	subscription := outbox.Subscription{
		Topic:               topic,
		ConsumerGroup:       "test-webhookdelay-e2e",
		CellID:              "test-webhookdelay-e2e",
		BrokerDelaySchedule: schedule,
	}

	// Explicit broker-accept gate (#1835): Setup declares the full topology
	// synchronously, so a real broker that rejected the quorum delay tiers'
	// at-least-once + reject-publish arg combo fails HERE with a clear error
	// instead of surfacing as a downstream subscriber-ready timeout. Idempotent
	// with the re-declare inside Subscribe below.
	require.NoError(t, sub.Setup(ctx, subscription),
		"broker must accept the quorum at-least-once delay-tier topology")

	var callCount atomic.Int32
	subCtx, subCancel := context.WithTimeout(ctx, testtime.CtxLong)
	defer subCancel()

	wrappedHandler := cb.Wrap(subscription, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount.Add(1)
		return outbox.Requeue(assert.AnError)
	})

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, subscription, wrappedHandler)
	}()

	waitForSubscriberReady(t, conn, mainQueue, subErrCh, testtime.EventuallyLong)

	entry := mustNewEntry(t, "test.webhookdelay.transient", []byte(`{"delay":"e2e"}`),
		outbox.WithID("evt-webhookdelay-e2e-001"), outbox.WithCreatedAt(time.Now().UTC()))
	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, topic, payload), "publish should succeed")

	// --- Consume the real DLX and verify the exhausted entry arrived intact ---
	dlxCh, err := conn.AcquireChannel()
	require.NoError(t, err)
	defer conn.ReleaseChannel(dlxCh)
	dlxMsgs, err := dlxCh.Consume(dlxQueue, "webhookdelay-dlx-consumer", true, false, false, false, nil)
	require.NoError(t, err, "consume from DLQ")

	var dlEntry outbox.Entry
	testwait.External(t, "amqp-webhookdelay-dlq", func() bool {
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
	}, testtime.D15s, testtime.D200ms,
		"exhausted webhook must reach the DLQ — handler called %d times", callCount.Load())

	assert.Equal(t, "evt-webhookdelay-e2e-001", dlEntry.ID(), "dead-lettered entry ID should match")
	assert.JSONEq(t, `{"delay":"e2e"}`, string(dlEntry.Payload()))
	assert.GreaterOrEqual(t, callCount.Load(), wantHandlerCalls,
		"handler must run once per scheduled attempt (immediate + one per tier) before exhaustion")

	subCancel()
	_ = sub.Close(context.Background())
}

// TestIntegration_ConnectionRecovery verifies that the Connection automatically
// reconnects after the broker forcibly closes all client connections.
//
// Uses rabbitmqctl close_all_connections (not container stop/start) to avoid
// port remapping issues — the broker stays on the same address.
func TestIntegration_ConnectionRecovery(t *testing.T) {
	conn, container, cleanup := startRabbitMQDedicatedContainer(t, Config{
		ReconnectBaseDelay:  testtime.D200ms,
		ReconnectMaxBackoff: testtime.D2s,
	})
	defer cleanup()

	ctx := context.Background()

	// 1. Verify initial healthy state.
	require.NoError(t, conn.Health(context.Background()), "initial Health should be nil")
	assert.Equal(t, StateConnected, conn.ConnectionStatus().State)

	// 2. Force-close all connections via rabbitmqctl.
	exitCode, _, err := container.Exec(ctx, []string{
		"rabbitmqctl", "close_all_connections", "integration-test",
	})
	require.NoError(t, err, "rabbitmqctl exec should not error")
	require.Equal(t, 0, exitCode, "rabbitmqctl should exit 0")

	// 3. Health() should return error during reconnect.
	testwait.External(t, "amqp-disconnect-detected", func() bool {
		return conn.Health(context.Background()) != nil
	}, testtime.EventuallyLong, testtime.MediumPoll,
		"Health() should report error after broker-forced disconnect")

	// Verify the state is Disconnected (not Terminal).
	status := conn.ConnectionStatus()
	assert.True(t, status.State == StateDisconnected || status.State == StateConnecting,
		"state should be Disconnected or Connecting during reconnect, got %s", status.State)

	// 4. Health() should recover after reconnect succeeds.
	testwait.External(t, "amqp-connection-healthy", func() bool {
		return conn.Health(context.Background()) == nil
	}, testtime.SelectAsyncSettle, testtime.SlowPoll,
		"Health() should recover after successful reconnect")

	assert.Equal(t, StateConnected, conn.ConnectionStatus().State,
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

// noopClaimer is a minimal idempotency.Claimer for testing that always
// returns ClaimAcquired with a noopReceipt.
type noopClaimer struct{}

func (n *noopClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimAcquired, &noopReceipt{}, nil
}

func (n *noopClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

type noopReceipt struct{}

func (n *noopReceipt) Commit(_ context.Context) error                  { return nil }
func (n *noopReceipt) Release(_ context.Context) error                 { return nil }
func (n *noopReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }
