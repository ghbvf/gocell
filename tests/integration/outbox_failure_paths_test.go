//go:build integration

package integration

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/outbox"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Failure-path integration tests for runtime/outbox.NewRelay + PGOutboxStore.
//
// These tests reinstate the failure-path coverage that was previously held in
// adapters/postgres/outbox_relay_integration_test.go (deleted in S30 Phase F)
// against the new wiring used by cmd/core-bundle:
//
//     pgStore := postgres.NewOutboxStore(pool.DB())
//     relay   := outboxruntime.NewRelay(pgStore, publisher, cfg)
//
// We exercise four scenarios that the Store-only conformance suite cannot
// observe because they live above the Store boundary:
//
//   1. transient publish failure → retry (attempts increment, eventual publish)
//   2. permanent publish failure → max attempts → dead (dead_at set)
//   3. concurrent relays → no double publish (cooperative SKIP LOCKED claim)
//   4. relay stop mid-publish → reclaim → another relay completes (takeover)
//
// Pattern: control failure deterministically via thin Publisher wrappers
// instead of injecting RabbitMQ failures, so the test exercises the relay's
// state machine + PG SQL rather than broker behavior. The capturingPublisher
// in outbox_fullchain_test.go stays the baseline for happy-path assertions;
// these tests add wrappers that flake or always fail.
// ---------------------------------------------------------------------------

// flakyPublisher fails the first failuresBeforeSuccess publishes for a given
// entry and succeeds afterwards. It records every published payload (including
// failed attempts) so the test can assert attempt counts.
type flakyPublisher struct {
	failuresBeforeSuccess int

	mu       sync.Mutex
	attempts map[string]int    // topic -> publish attempts seen
	success  map[string][]byte // topic -> payload of the successful publish
}

func newFlakyPublisher(failuresBeforeSuccess int) *flakyPublisher {
	return &flakyPublisher{
		failuresBeforeSuccess: failuresBeforeSuccess,
		attempts:              make(map[string]int),
		success:               make(map[string][]byte),
	}
}

func (p *flakyPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts[topic]++
	if p.attempts[topic] <= p.failuresBeforeSuccess {
		return assert.AnError // transient — relay should MarkRetry
	}
	p.success[topic] = append([]byte(nil), payload...)
	return nil
}

func (p *flakyPublisher) Close(_ context.Context) error { return nil }

func (p *flakyPublisher) attemptsFor(topic string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts[topic]
}

func (p *flakyPublisher) succeeded(topic string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.success[topic]
	return ok
}

// alwaysFailPublisher fails every publish call. Used to exercise the
// max-attempts → dead path.
type alwaysFailPublisher struct {
	mu       sync.Mutex
	attempts int
}

func (p *alwaysFailPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	p.mu.Lock()
	p.attempts++
	p.mu.Unlock()
	return assert.AnError
}

func (p *alwaysFailPublisher) Close(_ context.Context) error { return nil }

func (p *alwaysFailPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts
}

// stoppingPublisher blocks on the first publish call until released, then
// returns a transient error. The test uses this to simulate a relay that is
// stopped while it has a claim outstanding.
type stoppingPublisher struct {
	released chan struct{}
	entered  chan struct{}
	once     sync.Once
}

func newStoppingPublisher() *stoppingPublisher {
	return &stoppingPublisher{
		released: make(chan struct{}),
		entered:  make(chan struct{}, 1),
	}
}

func (p *stoppingPublisher) Publish(ctx context.Context, _ string, _ []byte) error {
	p.once.Do(func() { p.entered <- struct{}{} })
	select {
	case <-p.released:
		return assert.AnError // pretend transient so the entry doesn't dead-letter
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *stoppingPublisher) release() { close(p.released) }

func (p *stoppingPublisher) Close(_ context.Context) error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeOutboxEntry inserts a single outbox entry inside its own transaction
// and returns the entry ID.
func writeOutboxEntry(t *testing.T, pool *postgres.Pool, topic string, payload []byte) string {
	t.Helper()
	id := uuid.New().String()
	txm := postgres.NewTxManager(pool)
	writer := postgres.NewOutboxWriter()
	require.NoError(t,
		txm.RunInTx(context.Background(), func(txCtx context.Context) error {
			return writer.Write(txCtx, outbox.Entry{
				ID:        id,
				EventType: topic,
				Topic:     topic,
				Payload:   payload,
				CreatedAt: time.Now().UTC(),
			})
		}),
		"writeOutboxEntry must succeed")
	return id
}

// queryEntryStatus reads the current status (and attempts) of a single entry.
func queryEntryStatus(t *testing.T, pool *postgres.Pool, id string) (status string, attempts int, deadAt *time.Time) {
	t.Helper()
	err := pool.DB().QueryRow(context.Background(),
		"SELECT status, attempts, dead_at FROM outbox_entries WHERE id = $1", id,
	).Scan(&status, &attempts, &deadAt)
	require.NoError(t, err, "SELECT outbox_entries[%s]", id)
	return status, attempts, deadAt
}

// fastRelayConfig returns a RelayConfig with sub-second cadences suitable for
// failure-path tests. ClaimTTL > 2*PollInterval to satisfy the relay invariant.
func fastRelayConfig() outboxruntime.RelayConfig {
	return outboxruntime.RelayConfig{
		PollInterval:        50 * time.Millisecond,
		BatchSize:           10,
		MaxAttempts:         3,
		BaseRetryDelay:      20 * time.Millisecond,
		MaxRetryDelay:       100 * time.Millisecond,
		ClaimTTL:            300 * time.Millisecond,
		ReclaimInterval:     100 * time.Millisecond,
		RetentionPeriod:     1 * time.Hour,
		DeadRetentionPeriod: 24 * time.Hour,
	}
}

// runRelay starts relay in a background goroutine and returns a stopper that
// cancels the start ctx and waits for graceful Stop.
func runRelay(t *testing.T, relay *outboxruntime.Relay) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Start(ctx) }()
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = relay.Stop(stopCtx)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("relay.Start did not return after Stop")
		}
	}
}

// setupPGOnly is a lightweight helper for failure-path tests that don't need
// the RabbitMQ + Redis containers (we control publish outcomes via wrapper
// publishers, not real RMQ). Reuses setupPostgresContainer from the same
// integration package + applies the outbox migration.
func setupPGOnly(t *testing.T) (*postgres.Pool, func()) {
	t.Helper()
	pool, cleanup := setupPostgresContainer(t)
	migrator, err := postgres.NewMigrator(pool, postgres.MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "NewMigrator")
	require.NoError(t, migrator.Up(context.Background()), "migrations must apply")
	return pool, cleanup
}

// ---------------------------------------------------------------------------
// Test 1 — transient publish failure retried until success
// ---------------------------------------------------------------------------

func TestIntegration_PGRelay_TransientPublishRetry(t *testing.T) {
	pool, cleanup := setupPGOnly(t)
	defer cleanup()

	topic := "test.outbox.transient_retry"
	id := writeOutboxEntry(t, pool, topic, []byte(`{"phase":"transient"}`))

	pub := newFlakyPublisher(2) // fail twice, succeed third
	relay := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), pub, fastRelayConfig())

	stop := runRelay(t, relay)
	defer stop()

	require.Eventually(t, func() bool {
		status, _, _ := queryEntryStatus(t, pool, id)
		return status == "published"
	}, 5*time.Second, 50*time.Millisecond, "entry must reach published after transient retries")

	_, attempts, _ := queryEntryStatus(t, pool, id)
	assert.GreaterOrEqual(t, attempts, 2, "attempts must reflect retry count (publisher saw %d attempts)", pub.attemptsFor(topic))
	assert.True(t, pub.succeeded(topic), "publisher should have eventually succeeded")
}

// ---------------------------------------------------------------------------
// Test 2 — permanent publish failure escalates to dead at MaxAttempts
// ---------------------------------------------------------------------------

func TestIntegration_PGRelay_MaxAttemptsDeadLetter(t *testing.T) {
	pool, cleanup := setupPGOnly(t)
	defer cleanup()

	topic := "test.outbox.dead_letter"
	id := writeOutboxEntry(t, pool, topic, []byte(`{"phase":"dead"}`))

	pub := &alwaysFailPublisher{}
	cfg := fastRelayConfig()
	cfg.MaxAttempts = 3 // bound the test
	relay := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), pub, cfg)

	stop := runRelay(t, relay)
	defer stop()

	require.Eventually(t, func() bool {
		status, _, _ := queryEntryStatus(t, pool, id)
		return status == "dead"
	}, 5*time.Second, 50*time.Millisecond, "entry must reach dead after MaxAttempts publish failures")

	status, attempts, deadAt := queryEntryStatus(t, pool, id)
	assert.Equal(t, "dead", status)
	assert.Equal(t, cfg.MaxAttempts, attempts, "attempts must equal MaxAttempts")
	assert.NotNil(t, deadAt, "dead_at must be populated")
	assert.GreaterOrEqual(t, pub.count(), cfg.MaxAttempts, "publisher should see at least MaxAttempts calls")
}

// ---------------------------------------------------------------------------
// Test 3 — concurrent relays cooperate via SKIP LOCKED, no double publish
// ---------------------------------------------------------------------------

// countingPublisher records how many times each topic+payload pair was
// published, used to assert no-duplicate-publish across concurrent relays.
type countingPublisher struct {
	mu     sync.Mutex
	counts map[string]int
	total  atomic.Int64
}

func newCountingPublisher() *countingPublisher {
	return &countingPublisher{counts: make(map[string]int)}
}

func (p *countingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	p.counts[topic+"|"+string(payload)]++
	p.mu.Unlock()
	p.total.Add(1)
	return nil
}

func (p *countingPublisher) Close(_ context.Context) error { return nil }

func (p *countingPublisher) duplicates() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var dup []string
	for k, n := range p.counts {
		if n > 1 {
			dup = append(dup, k)
		}
	}
	return dup
}

func TestIntegration_PGRelay_ConcurrentRelaysNoDoublePublish(t *testing.T) {
	pool, cleanup := setupPGOnly(t)
	defer cleanup()

	const entryCount = 30
	topic := "test.outbox.concurrent_claim"
	ids := make([]string, entryCount)
	for i := range entryCount {
		ids[i] = writeOutboxEntry(t, pool, topic, []byte(`{"i":`+uuidShort()+`}`))
	}

	pub := newCountingPublisher()
	cfg := fastRelayConfig()
	cfg.BatchSize = 5 // small batch so the two relays interleave

	relayA := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), pub, cfg)
	relayB := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), pub, cfg)

	stopA := runRelay(t, relayA)
	defer stopA()
	stopB := runRelay(t, relayB)
	defer stopB()

	require.Eventually(t, func() bool {
		var n int
		require.NoError(t,
			pool.DB().QueryRow(context.Background(),
				"SELECT COUNT(*) FROM outbox_entries WHERE topic = $1 AND status = 'published'",
				topic,
			).Scan(&n),
			"COUNT(*) published")
		return n == entryCount
	}, 10*time.Second, 100*time.Millisecond, "all entries must be published exactly once")

	assert.Equal(t, int64(entryCount), pub.total.Load(),
		"total publish count must equal entry count (no duplicates from concurrent claim)")
	assert.Empty(t, pub.duplicates(), "no payload should be published twice")
}

// uuidShort returns a short UUID fragment to give each entry a unique payload
// without bringing in a fixture. Used inside a JSON literal.
func uuidShort() string { return `"` + uuid.New().String()[:8] + `"` }

// ---------------------------------------------------------------------------
// Test 4 — relay stop mid-publish + ReclaimStale → another relay takes over
// ---------------------------------------------------------------------------

func TestIntegration_PGRelay_StopMidPublishReclaimTakeover(t *testing.T) {
	pool, cleanup := setupPGOnly(t)
	defer cleanup()

	topic := "test.outbox.reclaim_takeover"
	id := writeOutboxEntry(t, pool, topic, []byte(`{"phase":"reclaim"}`))

	// Relay A blocks on first publish so the entry stays in `claiming` state
	// when we Stop it. ClaimTTL is short (300ms) so ReclaimStale on relay B
	// will recover it quickly.
	stuck := newStoppingPublisher()
	cfg := fastRelayConfig()
	cfg.MaxAttempts = 5

	relayA := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), stuck, cfg)

	stopACtx, cancelA := context.WithCancel(context.Background())
	doneA := make(chan error, 1)
	go func() { doneA <- relayA.Start(stopACtx) }()

	// Wait for relay A to actually begin its publish (i.e. claim the row).
	select {
	case <-stuck.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("relay A never reached publish")
	}

	// Verify the row is in claiming state in PG.
	var aClaimStatus string
	require.NoError(t, pool.DB().QueryRow(context.Background(),
		"SELECT status FROM outbox_entries WHERE id = $1", id,
	).Scan(&aClaimStatus))
	require.Equal(t, "claiming", aClaimStatus, "row must be claiming while relay A is stuck in publish")

	// Stop relay A. We release the publish first so the goroutine isn't wedged
	// (the relay's Stop has its own ctx cancellation but we want a clean exit).
	stuck.release()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	require.NoError(t, relayA.Stop(stopCtx))
	stopCancel()
	cancelA()
	<-doneA

	// Relay B starts fresh; ReclaimStale should rescue the row and a new
	// publish (now via a always-success publisher) will mark it published.
	successPub := newCountingPublisher()
	relayB := outboxruntime.NewRelay(postgres.NewOutboxStore(pool.DB()), successPub, cfg)
	stopB := runRelay(t, relayB)
	defer stopB()

	require.Eventually(t, func() bool {
		status, _, _ := queryEntryStatus(t, pool, id)
		return status == "published"
	}, 10*time.Second, 100*time.Millisecond, "relay B must reclaim and publish the orphaned entry")

	assert.GreaterOrEqual(t, successPub.total.Load(), int64(1), "relay B must publish at least once")
}
