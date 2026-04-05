//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ---------------------------------------------------------------------------
// Container helpers (inlined because per-adapter helpers are unexported)
// ---------------------------------------------------------------------------

func setupPostgresContainer(t *testing.T) (*postgres.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:15-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "get postgres connection string")

	pool, err := postgres.NewPool(ctx, postgres.Config{DSN: connStr})
	require.NoError(t, err, "create postgres pool")

	cleanup := func() {
		pool.Close()
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", termErr)
		}
	}
	return pool, cleanup
}

func setupRabbitMQContainer(t *testing.T) (*rabbitmq.Connection, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcrabbitmq.Run(ctx, "rabbitmq:3.12-management-alpine")
	require.NoError(t, err, "start rabbitmq container")

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "get rabbitmq amqp url")

	conn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ReconnectMaxBackoff: 5 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
	})
	require.NoError(t, err, "create rabbitmq connection")

	cleanup := func() {
		_ = conn.Close()
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate rabbitmq container: %v", termErr)
		}
	}
	return conn, cleanup
}

func setupRedisContainer(t *testing.T) (*redis.Client, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err, "get redis connection string")

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
		DialTimeout: 10 * time.Second,
	})
	require.NoError(t, err, "create redis client")

	cleanup := func() {
		_ = client.Close()
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate redis container: %v", termErr)
		}
	}
	return client, cleanup
}

// ---------------------------------------------------------------------------
// T25: TestIntegration_OutboxFullChain
// ---------------------------------------------------------------------------

// TestIntegration_OutboxFullChain validates the complete outbox pipeline:
//
//  1. Business write + outbox write (same transaction)
//  2. OutboxRelay polls unpublished entries and publishes to RabbitMQ
//  3. Subscriber consumes the message from RabbitMQ
//  4. IdempotencyChecker verifies idempotency semantics
//
// Infrastructure: PostgreSQL + RabbitMQ + Redis (3 testcontainers).
func TestIntegration_OutboxFullChain(t *testing.T) {
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
	migrator := postgres.NewMigrator(pool, postgres.MigrationsFS(), "schema_migrations")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	// ---------------------------------------------------------------
	// Step 3: Build components.
	// ---------------------------------------------------------------
	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter()
	pub := rabbitmq.NewPublisher(rmqConn)
	sub := rabbitmq.NewSubscriber(rmqConn, rabbitmq.SubscriberConfig{
		QueueName:       "outbox.fullchain.queue",
		PrefetchCount:   1,
		ShutdownTimeout: 5 * time.Second,
	})
	checker := redis.NewIdempotencyChecker(redisClient)

	relayCfg := postgres.DefaultRelayConfig()
	relayCfg.PollInterval = 200 * time.Millisecond // fast polling for test
	relayCfg.BatchSize = 10
	relay := postgres.NewOutboxRelay(pool.DB(), pub, relayCfg)

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
		Metadata:      map[string]string{"trace_id": "trace-full-chain-001"},
		CreatedAt:     time.Now().UTC(),
	}

	// Create a business table for the test.
	_, err := pool.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS test_orders (
		id   TEXT PRIMARY KEY,
		data TEXT NOT NULL
	)`)
	require.NoError(t, err, "create test_orders table")

	err = txm.RunInTx(ctx, func(txCtx context.Context) error {
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

	// Verify outbox entry exists and is unpublished.
	var published bool
	err = pool.DB().QueryRow(ctx, "SELECT published FROM outbox_entries WHERE id = $1", entryID).Scan(&published)
	require.NoError(t, err, "outbox entry should exist")
	assert.False(t, published, "outbox entry should be unpublished initially")

	// ---------------------------------------------------------------
	// Step 5: Start the subscriber BEFORE the relay so it is ready to
	//         receive messages when the relay publishes.
	// ---------------------------------------------------------------
	received := make(chan outbox.Entry, 1)
	subCtx, subCancel := context.WithTimeout(ctx, 30*time.Second)
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, topic, func(_ context.Context, e outbox.Entry) error {
			received <- e
			return nil
		})
	}()

	// Brief pause to let the subscriber bind its queue.
	time.Sleep(500 * time.Millisecond)

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
	var got outbox.Entry
	select {
	case got = <-received:
		// Success — message received.
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for message from subscriber")
	}

	// ---------------------------------------------------------------
	// Step 8: Verify received message payload matches the original.
	// ---------------------------------------------------------------
	assert.Equal(t, entryID, got.ID, "event ID should match")
	assert.Equal(t, "order-42", got.AggregateID, "aggregate ID should match")
	assert.Equal(t, "order", got.AggregateType, "aggregate type should match")
	assert.Equal(t, topic, got.EventType, "event type should match")
	assert.JSONEq(t,
		`{"orderId":"order-42","status":"created"}`,
		string(got.Payload),
		"payload should match original business event")

	// The relay serialises the full outbox.Entry as the AMQP body, so
	// Metadata round-trips through JSON.  The subscriber also injects
	// a "topic" key — verify trace_id survived.
	assert.Equal(t, "trace-full-chain-001", got.Metadata["trace_id"],
		"metadata trace_id should survive the full chain")

	// ---------------------------------------------------------------
	// Step 9: Verify the relay marked the outbox entry as published.
	// ---------------------------------------------------------------
	// Give the relay a moment to commit the UPDATE after publishing.
	time.Sleep(500 * time.Millisecond)

	err = pool.DB().QueryRow(ctx, "SELECT published FROM outbox_entries WHERE id = $1", entryID).Scan(&published)
	require.NoError(t, err)
	assert.True(t, published, "outbox entry should be marked as published after relay")

	// ---------------------------------------------------------------
	// Step 10: Verify idempotency semantics with RedisIdempotencyChecker.
	// ---------------------------------------------------------------
	idemKey := "idem:outbox-fullchain:" + entryID

	processed, err := checker.IsProcessed(ctx, idemKey)
	require.NoError(t, err)
	assert.False(t, processed, "key should NOT be processed before MarkProcessed")

	err = checker.MarkProcessed(ctx, idemKey, idempotency.DefaultTTL)
	require.NoError(t, err, "MarkProcessed should succeed")

	processed, err = checker.IsProcessed(ctx, idemKey)
	require.NoError(t, err)
	assert.True(t, processed, "key should be processed after MarkProcessed")

	// A second MarkProcessed should be idempotent (no error).
	err = checker.MarkProcessed(ctx, idemKey, idempotency.DefaultTTL)
	require.NoError(t, err, "duplicate MarkProcessed should be a no-op")

	// ---------------------------------------------------------------
	// Step 11: Verify duplicate message detection using the same key.
	// ---------------------------------------------------------------
	// Simulate receiving the same message again: check idempotency
	// before processing — should detect the duplicate.
	isDuplicate, err := checker.IsProcessed(ctx, idemKey)
	require.NoError(t, err)
	assert.True(t, isDuplicate, "same idempotency key should be detected as duplicate")

	// ---------------------------------------------------------------
	// Step 12: Verify a fresh key is NOT detected as processed.
	// ---------------------------------------------------------------
	freshKey := "idem:outbox-fullchain:" + uuid.New().String()
	isFresh, err := checker.IsProcessed(ctx, freshKey)
	require.NoError(t, err)
	assert.False(t, isFresh, "fresh idempotency key should not be processed")

	// ---------------------------------------------------------------
	// Cleanup: stop relay and subscriber.
	// ---------------------------------------------------------------
	relayCancel()
	_ = relay.Stop(ctx)
	subCancel()
	_ = sub.Close()
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
	migrator := postgres.NewMigrator(pool, postgres.MigrationsFS(), "schema_migrations")
	require.NoError(t, migrator.Up(ctx), "migrations must succeed")

	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter()

	// Mock publisher that captures published messages.
	mock := &capturingPublisher{messages: make(chan publishedMessage, 10)}

	relayCfg := postgres.DefaultRelayConfig()
	relayCfg.PollInterval = 100 * time.Millisecond
	relayCfg.BatchSize = 10
	relay := postgres.NewOutboxRelay(pool.DB(), mock, relayCfg)

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

		// The relay marshals the full outbox.Entry as JSON.
		var relayed outbox.Entry
		require.NoError(t, json.Unmarshal(msg.payload, &relayed), "payload should be valid JSON")
		assert.Equal(t, entryID, relayed.ID, "relayed entry ID should match")
		assert.Equal(t, "agg-mock-1", relayed.AggregateID)
		assert.JSONEq(t, `{"mock":true}`, string(relayed.Payload))

	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for mock publisher to receive message")
	}

	// Verify the entry was marked as published.
	time.Sleep(300 * time.Millisecond)

	var published bool
	err = pool.DB().QueryRow(ctx, "SELECT published FROM outbox_entries WHERE id = $1", entryID).Scan(&published)
	require.NoError(t, err)
	assert.True(t, published, "outbox entry should be marked published after relay")

	relayCancel()
	_ = relay.Stop(ctx)
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
