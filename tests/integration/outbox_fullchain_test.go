//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/tests/testutil"
)

const (
	// fullchainD20s is used as the subscriber receive timeout; not in testtime table.
	fullchainD20s = 20 * time.Second
)

type queueInspector interface {
	QueueInspect(name string) (amqp.Queue, error)
}

// ---------------------------------------------------------------------------
// Container helpers (inlined because per-adapter helpers are unexported)
// ---------------------------------------------------------------------------

func setupPostgresContainer(t *testing.T) (*postgres.Pool, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(testtime.CtxLong),
		),
	)
	require.NoError(t, err, "start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "get postgres connection string")

	pool, err := postgres.NewPool(ctx, postgres.Config{DSN: connStr})
	require.NoError(t, err, "create postgres pool")

	cleanup := func() {
		_ = pool.Close(ctx)
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", termErr)
		}
	}
	return pool, cleanup
}

func setupRabbitMQContainer(t *testing.T) (*rabbitmq.Connection, func()) {
	t.Helper()

	ctx := context.Background()

	container := testutil.StartRabbitMQContainer(t, ctx)

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get rabbitmq amqp url")

	conn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ReconnectMaxBackoff: testtime.SelectShutdown,
		ReconnectBaseDelay:  testtime.D500ms,
		ChannelPoolSize:     5,
		ConfirmTimeout:      testtime.SelectAsyncSettle,
	}, rabbitmq.WithConnectionClock(clock.Real()))
	require.NoError(t, err, "create rabbitmq connection")

	cleanup := func() {
		_ = conn.Close(ctx)
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate rabbitmq container: %v", termErr)
		}
	}
	return conn, cleanup
}

func setupRedisContainer(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcredis.Run(ctx, testutil.RedisImage)
	require.NoError(t, err, "start redis container")

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get redis connection string")
	connStr = testutil.LoopbackIPEndpoint(connStr)

	// Strip "redis://" prefix and trailing "/db" suffix to get host:port.
	addr := connStr
	if len(addr) > 8 && addr[:8] == "redis://" {
		addr = addr[8:]
	}
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == '/' {
			addr = addr[:i]
			break
		}
	}

	client, err := redis.NewClient(ctx, redis.Config{
		Addr:        addr,
		Mode:        redis.ModeStandalone,
		DialTimeout: testtime.SelectAsyncSettle,
	})
	require.NoError(t, err, "create redis client")

	cleanup := func() {
		_ = client.Close(ctx)
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate redis container: %v", termErr)
		}
	}
	return client, cleanup
}

func waitForSubscriberReady(t *testing.T, conn *rabbitmq.Connection, queueName string, subErrCh <-chan error, timeout time.Duration) {
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

// ---------------------------------------------------------------------------
// T25: TestIntegration_OutboxFullChain
// ---------------------------------------------------------------------------

// TestIntegration_OutboxFullChain validates the complete outbox pipeline:
//
//  1. Business write + outbox write (same transaction)
//  2. OutboxRelay polls unpublished entries and publishes to RabbitMQ
//  3. Subscriber consumes the message from RabbitMQ
//  4. IdempotencyClaimer verifies idempotency semantics
//
// Infrastructure: PostgreSQL + RabbitMQ + Redis (3 testcontainers).
func TestIntegration_OutboxFullChain(t *testing.T) {
	// Publish-side context carries observability IDs that will be injected
	// into entry.Observability by InjectObservabilityFromContext at write time.
	publishCtx := context.Background()
	publishCtx = ctxkeys.WithRequestID(publishCtx, "req-full-chain-001")
	publishCtx = ctxkeys.WithCorrelationID(publishCtx, "corr-full-chain-001")
	publishCtx = ctxkeys.WithTraceID(publishCtx, "trace-full-chain-001")

	// Infrastructure context is clean — no obs IDs. This ensures that
	// obs values in the consumer handler come from middleware restore,
	// not from context inheritance.
	ctx := context.Background()

	// ---------------------------------------------------------------
	// Step 1: Start all three containers.
	// ---------------------------------------------------------------
	pool, pgCleanup := setupPostgresContainer(t)
	defer pgCleanup()

	rmqConn, rmqCleanup := setupRabbitMQContainer(t)
	defer rmqCleanup()

	redisClient, redisCleanup := setupRedisContainer(t)
	defer redisCleanup()

	// ---------------------------------------------------------------
	// Step 2: Run migrations to create outbox_entries table.
	// ---------------------------------------------------------------
	migrator, mErr := postgres.NewMigrator(pool, testPostgresMigrationsFS(t), "schema_migrations")
	require.NoError(t, mErr, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	// ---------------------------------------------------------------
	// Step 3: Build components.
	// ---------------------------------------------------------------
	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter(clock.Real())
	pub := rabbitmq.NewPublisher(rmqConn, rabbitmq.WithPublisherClock(clock.Real()))
	sub := rabbitmq.NewSubscriber(rmqConn, rabbitmq.SubscriberConfig{
		QueueName:     "outbox.fullchain.queue",
		PrefetchCount: 1,
		DLXExchange:   "test.dlx",
		Clock:         clock.Real(),
	})
	claimer := redis.NewIdempotencyClaimer(redisClient)

	relayCfg := outboxruntime.DefaultRelayConfig()
	relayCfg.Clock = clock.Real()
	relayCfg.PollInterval = testtime.D200ms // fast polling for test
	relayCfg.BatchSize = 10
	relay := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB(), clock.Real()), pub, relayCfg)

	// ---------------------------------------------------------------
	// Step 4: Business write + outbox write in a single transaction.
	// ---------------------------------------------------------------
	entryID := uuid.New().String()
	topic := "test.outbox.fullchain"
	entry := outbox.Entry{
		ID:            entryID,
		AggregateID:   "order-42",
		AggregateType: "order",
		EventType:     topic,
		Payload:       []byte(`{"orderId":"order-42","status":"created"}`),
		Metadata:      map[string]string{"source": "integration-test"},
		CreatedAt:     time.Now().UTC(),
	}

	// Create a business table for the test.
	_, err := pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS test_orders (
		id   TEXT PRIMARY KEY,
		data TEXT NOT NULL
	)`)
	require.NoError(t, err, "create test_orders table")

	// Use publishCtx so InjectObservabilityFromContext picks up the obs IDs.
	err = txm.RunInTx(publishCtx, func(txCtx context.Context) error {
		// Business write.
		tx, ok := postgres.TxFromContext(txCtx)
		if !ok {
			t.Fatal("transaction must be in context")
		}
		if _, execErr := tx.Exec(txCtx,
			"INSERT INTO test_orders (id, data) VALUES ($1, $2)",
			"order-42", "full-chain-test",
		); execErr != nil {
			return execErr
		}
		// Outbox write (same transaction).
		return writer.Write(txCtx, entry)
	})
	require.NoError(t, err, "business + outbox write should succeed")

	// Verify business data was committed.
	var orderData string
	err = pool.DB().QueryRow(ctx, "SELECT data FROM test_orders WHERE id = $1", "order-42").Scan(&orderData)
	require.NoError(t, err, "business row should exist")
	assert.Equal(t, "full-chain-test", orderData)

	// Verify outbox entry exists and is pending.
	var status string
	err = pool.DB().QueryRow(ctx, "SELECT status FROM outbox_entries WHERE id = $1", entryID).Scan(&status)
	require.NoError(t, err, "outbox entry should exist")
	assert.Equal(t, "pending", status, "outbox entry should have status='pending' initially")

	// ---------------------------------------------------------------
	// Step 5: Start the subscriber BEFORE the relay so it is ready to
	//         receive messages when the relay publishes.
	// ---------------------------------------------------------------
	type observedDelivery struct {
		entry         outbox.Entry
		requestID     string
		correlationID string
		traceID       string
	}

	received := make(chan observedDelivery, 1)
	// Subscribe context is deliberately clean (no obs IDs). The only way
	// obs values reach the handler is through SubscriberWithMiddleware's
	// built-in outermost restore step (entry.Observability → ctxkeys).
	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.CtxLong)
	defer subCancel()

	wrappedSub := &outbox.SubscriberWithMiddleware{
		Inner:        sub,
		ConsumerBase: newIntegrationTestConsumerBaseWithClaimer(t, claimer, clock.Real()),
	}

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- wrappedSub.SubscribeEntry(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "fullchain-test", ContractID: "event.test.fullchain.v1", ContractKind: "event", ContractTransport: "memory"}, func(handlerCtx context.Context, e outbox.Entry) outbox.HandleResult {
			requestID, _ := ctxkeys.RequestIDFrom(handlerCtx)
			correlationID, _ := ctxkeys.CorrelationIDFrom(handlerCtx)
			traceID, _ := ctxkeys.TraceIDFrom(handlerCtx)
			received <- observedDelivery{
				entry:         e,
				requestID:     requestID,
				correlationID: correlationID,
				traceID:       traceID,
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	waitForSubscriberReady(t, rmqConn, "outbox.fullchain.queue", subErrCh, testtime.SelectShutdown)

	// ---------------------------------------------------------------
	// Step 6: Start the OutboxRelay in a background goroutine.
	// ---------------------------------------------------------------
	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()

	relayErrCh := make(chan error, 1)
	go func() {
		relayErrCh <- relay.Start(relayCtx)
	}()

	// ---------------------------------------------------------------
	// Step 7: Wait for the subscriber to receive the message.
	// ---------------------------------------------------------------
	var got observedDelivery
	select {
	case got = <-received:
		// Success — message received.
	case err := <-subErrCh:
		require.NoError(t, err, "subscriber exited before receiving the message")
		t.Fatal("subscriber exited before receiving the message")
	case err := <-relayErrCh:
		require.NoError(t, err, "relay exited before publishing the message")
		t.Fatal("relay exited before publishing the message")
	case <-time.After(fullchainD20s):
		t.Fatal("timed out waiting for message from subscriber")
	}

	// ---------------------------------------------------------------
	// Step 8: Verify received message payload matches the original.
	// ---------------------------------------------------------------
	assert.Equal(t, entryID, got.entry.ID, "event ID should match")
	assert.Equal(t, "order-42", got.entry.AggregateID, "aggregate ID should match")
	assert.Equal(t, "order", got.entry.AggregateType, "aggregate type should match")
	assert.Equal(t, topic, got.entry.EventType, "event type should match")
	assert.JSONEq(t,
		`{"orderId":"order-42","status":"created"}`,
		string(got.entry.Payload),
		"payload should match original business event")

	// The relay serialises the full outbox.Entry (including entry.Observability)
	// as the AMQP body. The outbox writer injects observability from context into
	// entry.Observability at write time; the consumer middleware restores it into
	// the handler context. Business metadata and observability are now distinct columns.
	assert.Equal(t, "integration-test", got.entry.Metadata["source"],
		"business metadata should be preserved")
	_, hasReqIDInMeta := got.entry.Metadata["request_id"]
	assert.False(t, hasReqIDInMeta,
		"request_id must not be in business metadata — it belongs in entry.Observability")

	assert.Equal(t, "req-full-chain-001", got.entry.Observability.RequestID,
		"request_id should be in entry.Observability, injected from context at write time")
	assert.Equal(t, "corr-full-chain-001", got.entry.Observability.CorrelationID,
		"correlation_id should be in entry.Observability")
	assert.Equal(t, "trace-full-chain-001", got.entry.Observability.TraceID,
		"trace_id should survive the full chain via entry.Observability")

	assert.Equal(t, "req-full-chain-001", got.requestID,
		"request_id should be restored into consumer handler context")
	assert.Equal(t, "corr-full-chain-001", got.correlationID,
		"correlation_id should be restored into consumer handler context")
	assert.Equal(t, "trace-full-chain-001", got.traceID,
		"trace_id should be restored into consumer handler context")

	// ---------------------------------------------------------------
	// Step 9: Verify the relay marked the outbox entry as published.
	// ---------------------------------------------------------------
	require.Eventually(t, func() bool {
		queryErr := pool.DB().QueryRow(ctx, "SELECT status FROM outbox_entries WHERE id = $1", entryID).Scan(&status)
		if queryErr != nil {
			return false
		}
		return status == "published"
	}, testtime.SelectShutdown, testtime.SlowPoll, "outbox entry should have status='published' after relay")

	// ---------------------------------------------------------------
	// Step 10: Verify idempotency semantics with IdempotencyClaimer.
	// ---------------------------------------------------------------
	idemKey := "idem:outbox-fullchain:" + entryID

	// First Claim should acquire a processing lease.
	state, receipt, err := claimer.Claim(ctx, idemKey, idempotency.DefaultLeaseTTL, idempotency.DefaultTTL)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state, "first Claim should acquire")
	require.NotNil(t, receipt, "ClaimAcquired must return a Receipt")

	// Commit the receipt to mark processing as done.
	err = receipt.Commit(ctx)
	require.NoError(t, err, "Commit should succeed")

	// ---------------------------------------------------------------
	// Step 11: Verify duplicate message detection using the same key.
	// ---------------------------------------------------------------
	// Second Claim on the same key should return ClaimDone.
	state, receipt2, err := claimer.Claim(ctx, idemKey, idempotency.DefaultLeaseTTL, idempotency.DefaultTTL)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimDone, state, "same idempotency key should be detected as done")
	require.NotNil(t, receipt2, "ClaimDone should return a non-acquired receipt")
	assert.ErrorIs(t, receipt2.Commit(ctx), idempotency.ErrNoClaimLease)

	// ---------------------------------------------------------------
	// Step 12: Verify a fresh key is NOT detected as processed.
	// ---------------------------------------------------------------
	freshKey := "idem:outbox-fullchain:" + uuid.New().String()
	freshState, freshReceipt, err := claimer.Claim(ctx, freshKey, idempotency.DefaultLeaseTTL, idempotency.DefaultTTL)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, freshState, "fresh idempotency key should acquire")
	require.NotNil(t, freshReceipt, "fresh key should return a Receipt")
	// Clean up: release the fresh key lease.
	_ = freshReceipt.Release(ctx)

	// ---------------------------------------------------------------
	// Cleanup: stop relay and subscriber.
	// ---------------------------------------------------------------
	relayCancel()
	_ = relay.Stop(ctx)
	subCancel()
	_ = sub.Close(context.Background())
}

// TestIntegration_OutboxFullChain_NoTrace validates that the outbox pipeline
// correctly handles the absence of trace context. When the originating HTTP
// handler has request_id and correlation_id but NO trace_id (e.g. tracing
// disabled or non-HTTP origin), those two IDs should still round-trip while
// trace_id remains absent from both entry metadata and consumer context.
//
// Satisfies acceptance criterion AC-5: "tracing disabled / non-HTTP context".
//
// Infrastructure: PostgreSQL + RabbitMQ (2 testcontainers, no Redis needed).
func TestIntegration_OutboxFullChain_NoTrace(t *testing.T) {
	// Publish-side context: request_id + correlation_id, NO trace_id.
	publishCtx := context.Background()
	publishCtx = ctxkeys.WithRequestID(publishCtx, "req-no-trace-001")
	publishCtx = ctxkeys.WithCorrelationID(publishCtx, "corr-no-trace-001")
	// Deliberately NOT setting trace_id — simulates tracing-disabled context.

	// Infrastructure context is clean — no obs IDs.
	ctx := context.Background()

	// ---------------------------------------------------------------
	// Step 1: Start containers.
	// ---------------------------------------------------------------
	pool, pgCleanup := setupPostgresContainer(t)
	defer pgCleanup()

	rmqConn, rmqCleanup := setupRabbitMQContainer(t)
	defer rmqCleanup()

	// ---------------------------------------------------------------
	// Step 2: Run migrations.
	// ---------------------------------------------------------------
	migrator, mErr := postgres.NewMigrator(pool, testPostgresMigrationsFS(t), "schema_migrations")
	require.NoError(t, mErr, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	// ---------------------------------------------------------------
	// Step 3: Build components.
	// ---------------------------------------------------------------
	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter(clock.Real())
	pub := rabbitmq.NewPublisher(rmqConn, rabbitmq.WithPublisherClock(clock.Real()))
	sub := rabbitmq.NewSubscriber(rmqConn, rabbitmq.SubscriberConfig{
		QueueName:     "outbox.fullchain.notrace.queue",
		PrefetchCount: 1,
		DLXExchange:   "test.dlx",
		Clock:         clock.Real(),
	})

	relayCfg := outboxruntime.DefaultRelayConfig()
	relayCfg.Clock = clock.Real()
	relayCfg.PollInterval = testtime.D200ms
	relayCfg.BatchSize = 10
	relay := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB(), clock.Real()), pub, relayCfg)

	// ---------------------------------------------------------------
	// Step 4: Business write + outbox write.
	// ---------------------------------------------------------------
	entryID := uuid.New().String()
	topic := "test.outbox.fullchain.notrace"
	entry := outbox.Entry{
		ID:            entryID,
		AggregateID:   "order-notrace-42",
		AggregateType: "order",
		EventType:     topic,
		Payload:       []byte(`{"orderId":"order-notrace-42","status":"created"}`),
		Metadata:      map[string]string{"source": "no-trace-test"},
		CreatedAt:     time.Now().UTC(),
	}

	_, err := pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS test_orders (
		id   TEXT PRIMARY KEY,
		data TEXT NOT NULL
	)`)
	require.NoError(t, err, "create test_orders table")

	// Use publishCtx so InjectObservabilityFromContext picks up the obs IDs.
	err = txm.RunInTx(publishCtx, func(txCtx context.Context) error {
		tx, ok := postgres.TxFromContext(txCtx)
		if !ok {
			t.Fatal("transaction must be in context")
		}
		if _, execErr := tx.Exec(txCtx,
			"INSERT INTO test_orders (id, data) VALUES ($1, $2) ON CONFLICT DO NOTHING",
			"order-notrace-42", "no-trace-test",
		); execErr != nil {
			return execErr
		}
		return writer.Write(txCtx, entry)
	})
	require.NoError(t, err, "business + outbox write should succeed")

	// ---------------------------------------------------------------
	// Step 5: Start subscriber, then relay.
	// ---------------------------------------------------------------
	type observedDelivery struct {
		entry         outbox.Entry
		requestID     string
		correlationID string
		traceID       string
		traceOK       bool
	}

	received := make(chan observedDelivery, 1)
	// Subscribe context is clean — no obs IDs.
	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.CtxLong)
	defer subCancel()

	wrappedSub := &outbox.SubscriberWithMiddleware{
		Inner:        sub,
		ConsumerBase: newIntegrationTestConsumerBase(t, clock.Real()),
	}

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- wrappedSub.SubscribeEntry(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "fullchain-notrace-test", ContractID: "event.test.fullchain.v1", ContractKind: "event", ContractTransport: "memory"}, func(handlerCtx context.Context, e outbox.Entry) outbox.HandleResult {
			requestID, _ := ctxkeys.RequestIDFrom(handlerCtx)
			correlationID, _ := ctxkeys.CorrelationIDFrom(handlerCtx)
			traceID, traceOK := ctxkeys.TraceIDFrom(handlerCtx)
			received <- observedDelivery{
				entry:         e,
				requestID:     requestID,
				correlationID: correlationID,
				traceID:       traceID,
				traceOK:       traceOK,
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()

	waitForSubscriberReady(t, rmqConn, "outbox.fullchain.notrace.queue", subErrCh, testtime.SelectShutdown)

	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()

	relayErrCh := make(chan error, 1)
	go func() {
		relayErrCh <- relay.Start(relayCtx)
	}()

	// ---------------------------------------------------------------
	// Step 6: Wait for the subscriber to receive the message.
	// ---------------------------------------------------------------
	var got observedDelivery
	select {
	case got = <-received:
		// Success.
	case err := <-subErrCh:
		require.NoError(t, err, "subscriber exited before receiving the message")
		t.Fatal("subscriber exited before receiving the message")
	case err := <-relayErrCh:
		require.NoError(t, err, "relay exited before publishing the message")
		t.Fatal("relay exited before publishing the message")
	case <-time.After(fullchainD20s):
		t.Fatal("timed out waiting for message from subscriber")
	}

	// ---------------------------------------------------------------
	// Step 7: Verify observability metadata — request_id and
	//         correlation_id survive; trace_id is absent.
	// ---------------------------------------------------------------
	assert.Equal(t, entryID, got.entry.ID, "event ID should match")

	// Business metadata preserved.
	assert.Equal(t, "no-trace-test", got.entry.Metadata["source"],
		"business metadata should be preserved")
	_, hasReqIDInMeta := got.entry.Metadata["request_id"]
	assert.False(t, hasReqIDInMeta,
		"request_id must not be in business metadata — it belongs in entry.Observability")

	// request_id and correlation_id round-trip via entry.Observability.
	assert.Equal(t, "req-no-trace-001", got.entry.Observability.RequestID,
		"request_id should be in entry.Observability, injected from context at write time")
	assert.Equal(t, "corr-no-trace-001", got.entry.Observability.CorrelationID,
		"correlation_id should be in entry.Observability")

	// trace_id should NOT be present in Observability (was never in context).
	assert.Empty(t, got.entry.Observability.TraceID,
		"trace_id should be empty in entry.Observability when not in originating context")

	// request_id and correlation_id should be restored into consumer context.
	assert.Equal(t, "req-no-trace-001", got.requestID,
		"request_id should be restored into consumer handler context")
	assert.Equal(t, "corr-no-trace-001", got.correlationID,
		"correlation_id should be restored into consumer handler context")

	// trace_id should be empty in consumer context.
	assert.Empty(t, got.traceID,
		"trace_id should be empty in consumer handler context when not in originating context")

	// ---------------------------------------------------------------
	// Cleanup.
	// ---------------------------------------------------------------
	relayCancel()
	_ = relay.Stop(ctx)
	subCancel()
	_ = sub.Close(context.Background())
}

// TestIntegration_OutboxWriteRelayMockPublisher is a lighter variant that
// validates the write-relay chain (postgres only) with a mock publisher,
// avoiding the need for RabbitMQ and Redis containers.
//
// This complements the full 3-container test above by exercising the
// database-level outbox mechanics in isolation.
func TestIntegration_OutboxWriteRelayMockPublisher(t *testing.T) {
	ctx := context.Background()

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()

	// Run migrations.
	migrator, mErr := postgres.NewMigrator(pool, testPostgresMigrationsFS(t), "schema_migrations")
	require.NoError(t, mErr, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter(clock.Real())

	// Mock publisher that captures published messages.
	mock := &capturingPublisher{messages: make(chan publishedMessage, 10)}

	relayCfg := outboxruntime.DefaultRelayConfig()
	relayCfg.Clock = clock.Real()
	relayCfg.PollInterval = testtime.SlowPoll
	relayCfg.BatchSize = 10
	relay := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB(), clock.Real()), mock, relayCfg)

	// Write outbox entry within a transaction.
	entryID := uuid.New().String()
	entry := outbox.Entry{
		ID:            entryID,
		AggregateID:   "agg-mock-1",
		AggregateType: "mock_aggregate",
		EventType:     "mock.created",
		Payload:       []byte(`{"mock":true}`),
		Metadata:      map[string]string{"test": "mock-relay"},
		CreatedAt:     time.Now().UTC(),
	}

	err := txm.RunInTx(ctx, func(txCtx context.Context) error {
		return writer.Write(txCtx, entry)
	})
	require.NoError(t, err, "outbox write should succeed")

	// Start relay.
	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()

	go func() {
		_ = relay.Start(relayCtx)
	}()

	// Wait for the mock publisher to capture the message.
	select {
	case msg := <-mock.messages:
		assert.Equal(t, "mock.created", msg.topic, "topic should match event type")

		// The relay serializes via outboxMessage where Payload is json.RawMessage
		// (embedded as a raw JSON object, not base64). Use a matching struct.
		var relayed struct {
			ID            string          `json:"id"`
			AggregateID   string          `json:"aggregateId"`
			AggregateType string          `json:"aggregateType"`
			EventType     string          `json:"eventType"`
			Payload       json.RawMessage `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(msg.payload, &relayed), "payload should be valid JSON")
		assert.Equal(t, entryID, relayed.ID, "relayed entry ID should match")
		assert.Equal(t, "agg-mock-1", relayed.AggregateID)
		assert.JSONEq(t, `{"mock":true}`, string(relayed.Payload))

	case <-time.After(testtime.SelectAsyncSettle):
		t.Fatal("timed out waiting for mock publisher to receive message")
	}

	// Verify the entry was marked as published.
	var pubStatus string
	require.Eventually(t, func() bool {
		queryErr := pool.DB().QueryRow(ctx, "SELECT status FROM outbox_entries WHERE id = $1", entryID).Scan(&pubStatus)
		return queryErr == nil && pubStatus == "published"
	}, testtime.SelectShutdown, testtime.SlowPoll, "outbox entry should have status='published' after relay")

	relayCancel()
	_ = relay.Stop(ctx)
}

// TestIntegration_OutboxObservability_ZeroRoundtrip writes an outbox entry
// with a zero ObservabilityMetadata, asserts the DB column is SQL NULL,
// then reclaims the entry via the relay store and asserts the round-tripped
// ClaimedEntry.Observability is the zero struct (no spurious fields, no
// unmarshal warnings). Covers the migration-012 documented contract that
// pre-existing rows or zero-valued writes read back as zero on the consumer
// side.
func TestIntegration_OutboxObservability_ZeroRoundtrip(t *testing.T) {
	ctx := context.Background()

	pool, cleanup := setupPostgresContainer(t)
	defer cleanup()

	migrator, mErr := postgres.NewMigrator(pool, testPostgresMigrationsFS(t), "schema_migrations")
	require.NoError(t, mErr)
	require.NoError(t, migrator.Up(ctx))

	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter(clock.Real())
	store := postgres.NewOutboxStore(pool.DB(), clock.Real())

	entryID := uuid.New().String()
	entry := outbox.Entry{
		ID:            entryID,
		AggregateID:   "agg-zero-obs",
		AggregateType: "zero_obs",
		EventType:     "test.zero.obs",
		Payload:       []byte(`{"k":"v"}`),
		// Metadata: producer-owned domain fields only.
		Metadata: map[string]string{"source": "zero-obs-test"},
		// Observability: explicit zero value.
		Observability: outbox.ObservabilityMetadata{},
		CreatedAt:     time.Now().UTC(),
	}

	err := txm.RunInTx(ctx, func(txCtx context.Context) error {
		return writer.Write(txCtx, entry)
	})
	require.NoError(t, err, "outbox write with zero observability should succeed")

	// Verify the observability column is SQL NULL (writer maps zero struct to NULL).
	var obsRaw *string
	err = pool.DB().QueryRow(ctx,
		"SELECT observability::text FROM outbox_entries WHERE id = $1", entryID,
	).Scan(&obsRaw)
	require.NoError(t, err, "observability column query should succeed")
	assert.Nil(t, obsRaw, "observability column must be SQL NULL for zero ObservabilityMetadata")

	// Claim back via the relay-side Store and verify Observability is the zero struct.
	claimed, err := store.ClaimPending(ctx, 10)
	require.NoError(t, err, "ClaimPending should succeed")
	require.Len(t, claimed, 1, "exactly one pending entry should be claimed")

	got := claimed[0]
	assert.Equal(t, entryID, got.ID)
	assert.True(t, got.Observability.IsZero(),
		"round-tripped Observability must be the zero struct for a NULL column")
	assert.Empty(t, got.Observability.RequestID)
	assert.Empty(t, got.Observability.CorrelationID)
	assert.Empty(t, got.Observability.TraceID)
	assert.Empty(t, got.Observability.TraceParent)
}

// publishedMessage captures a single Publish call.
type publishedMessage struct {
	topic   string
	payload []byte
}

// capturingPublisher implements outbox.Publisher by sending messages to a channel.
type capturingPublisher struct {
	messages chan publishedMessage
}

func (p *capturingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.messages <- publishedMessage{topic: topic, payload: payload}
	return nil
}

func (p *capturingPublisher) Close(_ context.Context) error { return nil }
