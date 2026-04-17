//go:build integration

package postgres

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// setupPGAndRMQ starts a PostgreSQL container and a RabbitMQ container,
// applies all migrations, and returns the pool, a rabbitmq.Publisher, and a
// cleanup function. The caller must invoke cleanup (or use t.Cleanup).
func setupPGAndRMQ(t *testing.T) (*Pool, *rabbitmq.Publisher, *rabbitmq.Connection, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()

	// Start PG container.
	pool, pgCleanup := setupPostgres(t)

	// Apply migrations.
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	// Start RMQ container.
	rmqContainer, err := tcrabbitmq.Run(ctx, testutil.RabbitMQImage)
	require.NoError(t, err, "failed to start rabbitmq container")

	amqpURL, err := rmqContainer.AmqpURL(ctx)
	require.NoError(t, err, "failed to get rabbitmq URL")

	rmqConn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
		ReconnectMaxBackoff: 5 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
	})
	require.NoError(t, err, "failed to create rabbitmq connection")

	pub := rabbitmq.NewPublisher(rmqConn)

	cleanup := func() {
		_ = rmqConn.Close()
		if err := rmqContainer.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate rmq container: %v", err)
		}
		pgCleanup()
	}

	return pool, pub, rmqConn, cleanup
}

// truncateOutbox removes all outbox_entries rows for test isolation.
func truncateOutbox(t *testing.T, pool *Pool) {
	t.Helper()
	_, err := pool.DB().Exec(context.Background(), "TRUNCATE outbox_entries")
	require.NoError(t, err, "TRUNCATE outbox_entries must succeed")
}

// writeTestEntry writes a single outbox entry within a transaction.
func writeTestEntry(t *testing.T, pool *Pool, topic string) string {
	t.Helper()
	entryID := uuid.New().String()
	txm := NewTxManager(pool)
	writer := NewOutboxWriter()
	err := txm.RunInTx(context.Background(), func(txCtx context.Context) error {
		return writer.Write(txCtx, outbox.Entry{
			ID:        entryID,
			EventType: topic,
			Topic:     topic,
			Payload:   []byte(`{"test":true}`),
			CreatedAt: time.Now(),
		})
	})
	require.NoError(t, err, "writeTestEntry must succeed")
	return entryID
}

// waitForStatus polls until the given outbox entry reaches the target status
// or the deadline is exceeded.
func waitForStatus(t *testing.T, pool *Pool, entryID, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var got string
		err := pool.DB().QueryRow(context.Background(),
			"SELECT status FROM outbox_entries WHERE id = $1", entryID).Scan(&got)
		if err == nil && got == status {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var got string
	_ = pool.DB().QueryRow(context.Background(),
		"SELECT status FROM outbox_entries WHERE id = $1", entryID).Scan(&got)
	t.Errorf("entry %s: want status=%q, got=%q (timeout after %s)", entryID, status, got, timeout)
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_HappyPath
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_HappyPath verifies that an outbox entry
// written to PG is relayed to RabbitMQ (exchange declared) and the entry
// transitions to status='published'.
func TestIntegration_OutboxRelay_HappyPath(t *testing.T) {
	pool, pub, rmqConn, cleanup := setupPGAndRMQ(t)
	defer cleanup()

	truncateOutbox(t, pool)

	const topic = "relay.happypath.v1"

	// Subscribe to the exchange to count received messages.
	// DLXExchange is required by the Subscriber API (Nack without DLX silently
	// discards messages, so the subscriber enforces it at construction time).
	subCfg := rabbitmq.SubscriberConfig{
		DLXExchange: "relay.happypath.dlx",
	}
	sub := rabbitmq.NewSubscriber(rmqConn, subCfg)
	var received atomic.Int32
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	// InitializeSubscription synchronously declares the exchange, DLX, queue
	// and binding before the relay publishes — avoids the race where messages
	// are published to a fanout exchange with no bound queue and are silently
	// dropped by RabbitMQ (fanout exchanges do not buffer for unbound queues).
	require.NoError(t,
		sub.InitializeSubscription(subCtx, topic, "cg-test-happypath"),
		"subscription topology must be pre-declared")

	go func() {
		_ = sub.Subscribe(subCtx, topic, func(ctx context.Context, e outbox.Entry) outbox.HandleResult {
			received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "cg-test-happypath")
	}()

	// Write one entry.
	entryID := writeTestEntry(t, pool, topic)

	// Start relay with fast poll.
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 200 * time.Millisecond
	cfg.BatchSize = 10
	cfg.MaxAttempts = 3

	relay := NewOutboxRelay(pool.DB(), pub, cfg)
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()

	go func() { _ = relay.Start(relayCtx) }()

	// Wait for entry to be published.
	waitForStatus(t, pool, entryID, "published", 15*time.Second)

	// Use Eventually so the subscriber goroutine has time to consume the message.
	// The relay marks the entry as 'published' before the subscriber processes
	// the delivery, so we poll instead of using a fixed sleep.
	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 10*time.Second, 100*time.Millisecond,
		"subscriber should have received at least one message")

	relayCancel()
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_TransientPublishFailureRetry
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_TransientPublishFailureRetry verifies that when
// the publisher returns a transient error (simulating a transient RabbitMQ
// publish failure such as channel error or transient broker issue), the
// relay's retry state machine backs off, retries, and eventually publishes
// successfully when the publisher recovers.
//
// Scope: this test exercises the relay's retry state machine against
// publisher error returns. It does NOT cover RabbitMQ TCP-level
// disconnect/reconnect — that is the RabbitMQ adapter's responsibility,
// tested in adapters/rabbitmq/integration_test.go.
func TestIntegration_OutboxRelay_TransientPublishFailureRetry(t *testing.T) {
	pool, _, _, cleanup := setupPGAndRMQ(t)
	defer cleanup()

	truncateOutbox(t, pool)

	const topic = "relay.retry.v1"

	// Use a counting publisher: returns transient errors for the first 2 Publish
	// calls then succeeds. This simulates a transient broker-side publish failure
	// (e.g., channel error) without requiring a real RabbitMQ TCP disconnect.
	var callCount atomic.Int32
	pub := &countingPublisher{
		failUntil: 2,
		calls:     &callCount,
	}

	entryID := writeTestEntry(t, pool, topic)

	cfg := DefaultRelayConfig()
	cfg.PollInterval = 150 * time.Millisecond
	cfg.MaxAttempts = 5
	cfg.BaseRetryDelay = 100 * time.Millisecond
	cfg.MaxRetryDelay = 500 * time.Millisecond

	relay := NewOutboxRelay(pool.DB(), pub, cfg)
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()

	go func() { _ = relay.Start(relayCtx) }()

	// After the relay backs off and retries, the entry should reach published.
	waitForStatus(t, pool, entryID, "published", 20*time.Second)
	relayCancel()
	assert.GreaterOrEqual(t, callCount.Load(), int32(3),
		"should have had at least 3 publish attempts (2 transient failures + 1 success)")
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_MaxAttemptsDeadLetter
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_MaxAttemptsDeadLetter verifies that when a
// publisher always fails, an entry transitions to status='dead' after
// MaxAttempts is exhausted.
func TestIntegration_OutboxRelay_MaxAttemptsDeadLetter(t *testing.T) {
	pool, _, _, cleanup := setupPGAndRMQ(t)
	defer cleanup()

	truncateOutbox(t, pool)

	const topic = "relay.deadletter.v1"

	// Publisher always fails.
	pub := &failingPublisher{}

	entryID := writeTestEntry(t, pool, topic)

	cfg := DefaultRelayConfig()
	cfg.PollInterval = 100 * time.Millisecond
	cfg.MaxAttempts = 3
	cfg.BaseRetryDelay = 50 * time.Millisecond
	cfg.MaxRetryDelay = 200 * time.Millisecond

	relay := NewOutboxRelay(pool.DB(), pub, cfg)
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()

	go func() { _ = relay.Start(relayCtx) }()

	// After MaxAttempts failures, status should be 'dead'.
	waitForStatus(t, pool, entryID, "dead", 20*time.Second)
	relayCancel()
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_ConcurrentRelayNoDoubleClaim
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_ConcurrentRelayNoDoubleClaim starts two relay
// instances against the same PG. FOR UPDATE SKIP LOCKED ensures each entry
// is claimed by exactly one relay; the total published count must equal the
// number of entries written.
func TestIntegration_OutboxRelay_ConcurrentRelayNoDoubleClaim(t *testing.T) {
	pool, pub, _, cleanup := setupPGAndRMQ(t)
	defer cleanup()

	truncateOutbox(t, pool)

	const (
		topic      = "relay.concurrent.v1"
		entryCount = 20
	)

	// Write entries.
	ids := make([]string, entryCount)
	for i := range entryCount {
		ids[i] = writeTestEntry(t, pool, topic)
	}

	// Counting publisher to detect double-publish.
	var totalPublished atomic.Int32
	pub2 := &countingSuccessPublisher{delegate: pub, counter: &totalPublished}

	cfg := DefaultRelayConfig()
	cfg.PollInterval = 100 * time.Millisecond
	cfg.BatchSize = 5
	cfg.MaxAttempts = 3

	relay1 := NewOutboxRelay(pool.DB(), pub2, cfg)
	relay2 := NewOutboxRelay(pool.DB(), pub2, cfg)

	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()

	go func() { _ = relay1.Start(relayCtx) }()
	go func() { _ = relay2.Start(relayCtx) }()

	// Wait for all entries to be published.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var publishedCount int
		err := pool.DB().QueryRow(context.Background(),
			"SELECT count(*) FROM outbox_entries WHERE status = 'published'").Scan(&publishedCount)
		if err == nil && publishedCount == entryCount {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	relayCancel()

	// Verify all entries are published.
	var publishedCount int
	err := pool.DB().QueryRow(context.Background(),
		"SELECT count(*) FROM outbox_entries WHERE status = 'published'").Scan(&publishedCount)
	require.NoError(t, err)
	assert.Equal(t, entryCount, publishedCount, "all entries should be published exactly once")

	// No double-publish: totalPublished by both relays should equal entryCount.
	assert.Equal(t, int32(entryCount), totalPublished.Load(),
		"total publish calls should equal entry count (no double-publish)")
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_CleanShutdownMidPublish
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_CleanShutdownMidPublish verifies that when
// Stop() is called while entries are being processed, claimed entries do NOT
// remain permanently stuck in the 'claiming' state.
// Note: Stop() does NOT immediately release claims; reclaimStale (TTL-based
// recovery) is responsible for picking up stuck 'claiming' entries after
// claimTTL + ReclaimInterval elapses. A second relay instance is started after
// the first one stops to run reclaimStale — this simulates a pod restart or
// rolling update scenario where a new relay takes over from a crashed one.
func TestIntegration_OutboxRelay_CleanShutdownMidPublish(t *testing.T) {
	pool, pub, _, cleanup := setupPGAndRMQ(t)
	defer cleanup()

	truncateOutbox(t, pool)

	const (
		topic      = "relay.shutdown.v1"
		entryCount = 10
	)

	for range entryCount {
		writeTestEntry(t, pool, topic)
	}

	// Use a slow publisher to increase the chance of entries being in 'claiming'
	// when Stop() is called.
	slowPub := &slowPublisher{
		delegate: pub,
		delay:    300 * time.Millisecond,
	}

	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond
	cfg.BatchSize = entryCount
	cfg.MaxAttempts = 5
	cfg.ClaimTTL = 2 * time.Second // short TTL so reclaimStale runs quickly
	cfg.ReclaimInterval = 500 * time.Millisecond

	relay := NewOutboxRelay(pool.DB(), slowPub, cfg)
	relayCtx, relayCancel := context.WithCancel(context.Background())

	go func() { _ = relay.Start(relayCtx) }()

	// Let relay run briefly then stop (while some entries are still 'claiming').
	time.Sleep(200 * time.Millisecond)
	relayCancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, relay.Stop(stopCtx), "relay.Stop should return nil")

	// Start a second relay (simulating pod restart / takeover).
	// Its reclaimLoop will recover any entries stuck in 'claiming' once
	// ClaimTTL elapses. The second relay uses the real publisher so recovered
	// entries can be completed; it does not need a subscriber for this assertion.
	cfg2 := DefaultRelayConfig()
	cfg2.PollInterval = 100 * time.Millisecond
	cfg2.BatchSize = entryCount
	cfg2.MaxAttempts = 5
	cfg2.ClaimTTL = 2 * time.Second
	cfg2.ReclaimInterval = 300 * time.Millisecond

	relay2 := NewOutboxRelay(pool.DB(), pub, cfg2)
	relay2Ctx, relay2Cancel := context.WithCancel(context.Background())
	defer relay2Cancel()
	go func() { _ = relay2.Start(relay2Ctx) }()

	// Wait for reclaimStale to run on relay2 (ClaimTTL + 2*ReclaimInterval + buffer).
	// After this window, all entries should have left 'claiming'.
	time.Sleep(cfg2.ClaimTTL + 2*cfg2.ReclaimInterval + 500*time.Millisecond)
	relay2Cancel()

	// No entries should be permanently stuck in 'claiming'.
	var claimingCount int
	err := pool.DB().QueryRow(context.Background(),
		"SELECT count(*) FROM outbox_entries WHERE status = 'claiming'").Scan(&claimingCount)
	require.NoError(t, err)
	assert.Equal(t, 0, claimingCount,
		"no entries should remain in 'claiming' after relay stops and reclaimTTL passes")
}

// ---------------------------------------------------------------------------
// TestIntegration_OutboxRelay_BrokerTCPDisconnectRecovery
// ---------------------------------------------------------------------------

// TestIntegration_OutboxRelay_BrokerTCPDisconnectRecovery verifies that the
// outbox relay survives a real RabbitMQ TCP disconnect (container stop) and
// recovers automatically when the broker restarts (container start).
//
// Fault model: testcontainers Stop/Start — real OS-level TCP teardown, not a
// publisher-error mock. This is a different fault class from
// TransientPublishFailureRetry (which exercises the relay's retry state machine
// against publisher error returns). Together the two tests form complete
// "publish error mock" + "real broker TCP disconnect" coverage.
//
// Test steps:
//  1. Batch 1 (2 entries): relay publishes successfully before broker stop.
//  2. Broker STOP: container.Stop terminates the TCP connection.
//  3. Batch 2 (2 entries): relay publish fails; entries retry with backoff.
//  4. Broker START: container.Start brings the broker back up.
//  5. Connection auto-reconnects (rabbitmq.Connection.reconnectLoop).
//  6. Relay picks up batch 2 again and publishes; all entries reach 'published'.
//
// Total budget: ~60s (container stop/start ~5-15s, retry window ~20s).
func TestIntegration_OutboxRelay_BrokerTCPDisconnectRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TCP disconnect test in short mode")
	}
	testutil.RequireDocker(t)

	ctx := context.Background()

	// Start PG container and apply migrations.
	pool, pgCleanup := setupPostgres(t)
	defer pgCleanup()

	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	// Start RMQ container — keep a direct reference for Stop/Start.
	rmqContainer, err := tcrabbitmq.Run(ctx, testutil.RabbitMQImage)
	require.NoError(t, err, "failed to start rabbitmq container")
	defer func() {
		if termErr := rmqContainer.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate rmq container: %v", termErr)
		}
	}()

	amqpURL, err := rmqContainer.AmqpURL(ctx)
	require.NoError(t, err, "failed to get rabbitmq URL")

	// Create connection with short reconnect backoff so the test runs quickly
	// after the broker comes back up.
	rmqConn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
		ReconnectMaxBackoff: 3 * time.Second,
	})
	require.NoError(t, err, "failed to create rabbitmq connection")
	defer func() { _ = rmqConn.Close() }()

	pub := rabbitmq.NewPublisher(rmqConn)

	const topic = "relay.tcpdisconnect.v1"

	// Pre-declare topology so batch 1 messages are queued by the broker even
	// before the subscriber goroutine starts consuming.
	subCfg := rabbitmq.SubscriberConfig{
		DLXExchange: "relay.tcpdisconnect.dlx",
	}
	sub := rabbitmq.NewSubscriber(rmqConn, subCfg)
	require.NoError(t,
		sub.InitializeSubscription(ctx, topic, "cg-test-tcpdisconnect"),
		"subscription topology must be pre-declared")

	var received atomic.Int32
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	go func() {
		_ = sub.Subscribe(subCtx, topic, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			received.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "cg-test-tcpdisconnect")
	}()

	truncateOutbox(t, pool)

	// ── Batch 1: write and publish before broker stop ──
	batch1IDs := make([]string, 2)
	for i := range batch1IDs {
		batch1IDs[i] = writeTestEntry(t, pool, topic)
	}

	// Relay config: fast poll + short retry so broker-down retries are rapid.
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 300 * time.Millisecond
	cfg.BatchSize = 10
	cfg.MaxAttempts = 20 // high enough to survive the broker-down window
	cfg.BaseRetryDelay = 500 * time.Millisecond
	cfg.MaxRetryDelay = 3 * time.Second
	cfg.ClaimTTL = 10 * time.Second
	cfg.ReclaimInterval = 2 * time.Second

	relay := NewOutboxRelay(pool.DB(), pub, cfg)
	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()

	go func() { _ = relay.Start(relayCtx) }()

	// Wait for batch 1 to be published before stopping the broker.
	for _, id := range batch1IDs {
		waitForStatus(t, pool, id, "published", 15*time.Second)
	}
	assert.Eventually(t, func() bool {
		return received.Load() >= int32(len(batch1IDs))
	}, 10*time.Second, 200*time.Millisecond,
		"subscriber should have received batch 1 before broker stop")

	// ── Stop broker: real TCP teardown ──
	t.Log("stopping RMQ container to simulate TCP disconnect")
	stopTimeout := 5 * time.Second
	require.NoError(t, rmqContainer.Stop(ctx, &stopTimeout), "rmq container stop must succeed")
	t.Log("RMQ container stopped")

	// ── Batch 2: write while broker is down ──
	batch2IDs := make([]string, 2)
	for i := range batch2IDs {
		batch2IDs[i] = writeTestEntry(t, pool, topic)
	}

	// Allow the relay to attempt and fail batch 2 while broker is down.
	// Entries should be retrying (attempts > 0) but not dead.
	time.Sleep(3 * time.Second)

	// Verify batch 2 entries are retrying (attempts > 0) and not yet published.
	for _, id := range batch2IDs {
		var status string
		var attempts int
		err := pool.DB().QueryRow(ctx,
			"SELECT status, attempts FROM outbox_entries WHERE id = $1", id).
			Scan(&status, &attempts)
		require.NoError(t, err, "should be able to query entry %s", id)
		assert.NotEqual(t, "published", status,
			"batch2 entry %s should not be published while broker is down", id)
		assert.Greater(t, attempts, 0,
			"batch2 entry %s should have retry attempts > 0 (relay is retrying)", id)
		t.Logf("batch2 entry %s: status=%s attempts=%d (broker down, relay retrying)", id, status, attempts)
	}

	// ── Restart broker ──
	t.Log("restarting RMQ container")
	require.NoError(t, rmqContainer.Start(ctx), "rmq container start must succeed")
	t.Log("RMQ container restarted")

	// The rabbitmq.Connection.reconnectLoop detects the TCP close event and
	// automatically re-dials once the container is back. The relay's publisher
	// will succeed on the next poll after reconnection. The subscriber also
	// recovers via WaitConnected + re-subscribe loop.

	// ── Wait for batch 2 to be published after broker recovery ──
	for _, id := range batch2IDs {
		waitForStatus(t, pool, id, "published", 45*time.Second)
	}

	// Wait for subscriber to consume all messages (batch 1 already counted).
	totalExpected := int32(len(batch1IDs) + len(batch2IDs))
	assert.Eventually(t, func() bool {
		return received.Load() >= totalExpected
	}, 20*time.Second, 300*time.Millisecond,
		"subscriber should receive all %d messages (batch1+batch2) after broker recovery", totalExpected)

	relayCancel()
	_ = sub.Close()

	// ── Final assertions ──
	var publishedCount int
	err = pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM outbox_entries WHERE status = 'published'").Scan(&publishedCount)
	require.NoError(t, err)
	assert.Equal(t, int(totalExpected), publishedCount,
		"all %d entries (batch1+batch2) must reach status='published'", totalExpected)

	// Verify batch 2 entries have attempts > 0 proving retry mechanism fired.
	for _, id := range batch2IDs {
		var attempts int
		err := pool.DB().QueryRow(ctx,
			"SELECT attempts FROM outbox_entries WHERE id = $1", id).Scan(&attempts)
		require.NoError(t, err)
		assert.Greater(t, attempts, 0,
			"batch2 entry %s must have attempts > 0 (relay retried during broker outage)", id)
	}

	t.Logf("broker TCP disconnect/recovery test passed: %d total messages delivered, batch2 retried after outage", totalExpected)
}

// ---------------------------------------------------------------------------
// Test helper publishers
// ---------------------------------------------------------------------------

// countingPublisher returns a transient error for the first N calls, then succeeds.
// It simulates a publisher that recovers after a series of transient publish failures.
type countingPublisher struct {
	failUntil int32
	calls     *atomic.Int32
}

func (p *countingPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	n := p.calls.Add(1)
	if n <= p.failUntil {
		return errcode.New(ErrAdapterPGPublish, "simulated transient publish failure")
	}
	return nil
}

// failingPublisher always returns an error, simulating a permanently failing publisher.
type failingPublisher struct{}

func (p *failingPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errcode.New(ErrAdapterPGPublish, "simulated permanent publish failure")
}

// countingSuccessPublisher wraps a real publisher and counts successful publishes.
type countingSuccessPublisher struct {
	delegate outbox.Publisher
	counter  *atomic.Int32
}

func (p *countingSuccessPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := p.delegate.Publish(ctx, topic, payload); err != nil {
		return err
	}
	p.counter.Add(1)
	return nil
}

// slowPublisher wraps a publisher and adds an artificial delay.
type slowPublisher struct {
	delegate outbox.Publisher
	delay    time.Duration
}

func (p *slowPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.delegate.Publish(ctx, topic, payload)
}
