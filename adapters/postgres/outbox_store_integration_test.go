//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	rout "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// TestPGOutboxStore_ConformanceSuite verifies that PGOutboxStore satisfies the
// full Store conformance suite defined in runtime/outbox/outboxtest.
//
// This test requires a running PostgreSQL container (Docker).
// Build tag: //go:build integration — excluded from `go test -short` runs.
func TestPGOutboxStore_ConformanceSuite(t *testing.T) {
	// setupPostgres is defined in integration_test.go (same package, integration build tag).
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_store_conformance")
	require.NoError(t, err, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	factory := func(t *testing.T, seed []rout.ClaimedEntry) rout.Store {
		t.Helper()
		// Truncate for test isolation — each conformance subcase gets a clean table.
		_, truncErr := pool.DB().Exec(ctx, "TRUNCATE outbox_entries")
		require.NoError(t, truncErr, "TRUNCATE outbox_entries must succeed")

		for _, ce := range seed {
			insertSeedRow(t, pool, ce)
		}
		return NewOutboxStore(pool.DB(), clock.Real())
	}

	outboxtest.RunStoreConformanceSuite(t, factory)
}

func TestPGOutboxStore_RelayPublishesRollbackStateBeforeAudit(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_store_order")
	require.NoError(t, err, "NewMigrator should succeed")
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	base := time.Now().UTC()
	insertSeedRow(t, pool, rout.ClaimedEntry{Entry: kout.Entry{
		ID:            "evt-state-sync",
		AggregateID:   "cfg-app-name",
		AggregateType: "config_entry",
		EventType:     "event.config.entry-upserted.v1",
		Payload:       []byte(`{"key":"app.name","value":"v1","version":2}`),
		CreatedAt:     base,
	}})
	insertSeedRow(t, pool, rout.ClaimedEntry{Entry: kout.Entry{
		ID:            "evt-rollback-audit",
		AggregateID:   "cfg-app-name",
		AggregateType: "config_entry",
		EventType:     "event.config.rollback.v1",
		Payload:       []byte(`{"key":"app.name","targetVersion":1,"newVersion":2}`),
		CreatedAt:     base.Add(time.Microsecond),
	}})

	store := NewOutboxStore(pool.DB(), clock.Real())
	pub := &recordingPublisher{}
	relay := rout.NewRelay(store, pub, rout.RelayConfig{
		PollInterval:        testtime.FastPoll,
		ReclaimInterval:     testtime.MediumPoll,
		BatchSize:           10,
		MaxAttempts:         3,
		BaseRetryDelay:      time.Millisecond,
		MaxRetryDelay:       testtime.D10ms,
		ClaimTTL:            testtime.SlowPoll,
		RetentionPeriod:     time.Hour,
		DeadRetentionPeriod: time.Hour,
		CleanupWaitFloor:    testtime.MediumPoll,
		Clock:               clock.Real(),
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- relay.Start(runCtx) }()
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer stopCancel()
		require.NoError(t, relay.Stop(stopCtx))
		cancel()
		require.NoError(t, <-errCh)
	})

	require.Eventually(t, func() bool {
		return len(pub.Topics()) >= 2
	}, testtime.D2s, testtime.D10ms)
	topics := pub.Topics()
	require.GreaterOrEqual(t, len(topics), 2)
	assert.Equal(t, []string{
		"event.config.entry-upserted.v1",
		"event.config.rollback.v1",
	}, topics[:2])
}

type recordingPublisher struct {
	mu     sync.Mutex
	topics []string
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	return nil
}

func (p *recordingPublisher) Close(_ context.Context) error { return nil }

func (p *recordingPublisher) Topics() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.topics))
	copy(out, p.topics)
	return out
}

// TestPGOutboxStore_ReclaimStale_RespectsBatchLimit verifies B2-A-06: a
// backlog of stale claiming rows must not produce a single multi-second
// UPDATE that holds locks blocking VACUUM and replication. ReclaimStale caps
// at reclaimBatchSize per call; relay's tick loop drains residual.
func TestPGOutboxStore_ReclaimStale_RespectsBatchLimit(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_reclaim_limit")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	const seedCount = reclaimBatchSize + 500 // 1500 stale claiming rows

	// Seed `seedCount` rows in claiming state with claimed_at far in the past.
	// Batched INSERT keeps the test under a couple seconds even at 1500 rows.
	tx, err := pool.DB().Begin(ctx)
	require.NoError(t, err)
	for i := 0; i < seedCount; i++ {
		_, execErr := tx.Exec(ctx,
			`INSERT INTO outbox_entries
			(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, claimed_at, lease_id)
			VALUES ($1, 'agg-stale', 'test', 'ev', 't', $2, NULL, $3, 'claiming', $3, gen_random_uuid())`,
			"e-stale-"+strconv.Itoa(i), []byte(`{"i":`+strconv.Itoa(i)+`}`),
			time.Now().Add(-time.Hour))
		require.NoError(t, execErr)
	}
	require.NoError(t, tx.Commit(ctx))

	store := NewOutboxStore(pool.DB(), clock.Real())

	// First ReclaimStale must reclaim exactly reclaimBatchSize.
	count, err := store.ReclaimStale(ctx, time.Minute, 99, time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, reclaimBatchSize, count,
		"first reclaim must cap at reclaimBatchSize, not full backlog")

	// Second call drains the residual.
	count2, err := store.ReclaimStale(ctx, time.Minute, 99, time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Equal(t, seedCount-reclaimBatchSize, count2,
		"second reclaim drains residual")

	// Third call is a no-op.
	count3, err := store.ReclaimStale(ctx, time.Minute, 99, time.Millisecond, time.Second)
	require.NoError(t, err)
	assert.Zero(t, count3, "third call has nothing to reclaim")
}

// TestPGOutboxStore_Fencing_ReclaimedRowSurvivesStaleMark exercises the PG
// fencing CAS end-to-end: worker A claims, reclaim fires, worker A's stale
// MarkPublished must miss while worker B's lease is preserved. (B2-A-01)
func TestPGOutboxStore_Fencing_ReclaimedRowSurvivesStaleMark(t *testing.T) {
	pool, cleanup := setupPostgres(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(t), "schema_migrations_fencing")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply")

	insertSeedRow(t, pool, rout.ClaimedEntry{Entry: kout.Entry{
		ID:            "evt-fencing-race",
		AggregateID:   "agg-1",
		AggregateType: "test",
		EventType:     "test.v1",
		Topic:         "test.v1",
		Payload:       []byte(`{"x":1}`),
		CreatedAt:     time.Now().UTC(),
	}})

	store := NewOutboxStore(pool.DB(), clock.Real())

	// Worker A claims.
	a, err := store.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, a, 1)
	leaseA := a[0].LeaseID

	// Force claimed_at into the past so ReclaimStale catches it.
	_, err = pool.DB().Exec(ctx,
		"UPDATE outbox_entries SET claimed_at = $1 WHERE id = 'evt-fencing-race'",
		time.Now().Add(-time.Hour))
	require.NoError(t, err)

	// Reclaim sweep: TTL=1s vs claimed_at 1h ago → stale → back to pending,
	// lease cleared.
	count, err := store.ReclaimStale(ctx, time.Second, 99, time.Millisecond, time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Wait until the row becomes pending again (reclaim backoff elapsed)
	// and ClaimPending returns it. The backoff with attempts=1 + baseDelay=1ms
	// is well under 100ms.
	var b []rout.ClaimedEntry
	require.Eventually(t, func() bool {
		var claimErr error
		b, claimErr = store.ClaimPending(ctx, 10)
		return claimErr == nil && len(b) == 1
	}, testtime.D5s, testtime.D50ms, "row must become claimable after reclaim backoff")

	require.Len(t, b, 1)
	leaseB := b[0].LeaseID
	require.NotEqual(t, leaseA, leaseB, "worker B must receive a fresh lease distinct from A")

	// Worker A's stale MarkPublished MUST NOT win — `require.False` so a
	// fencing violation halts the test cleanly with a single root-cause
	// failure (no cascading false-positives from a subsequent re-publish
	// against an already-published row).
	updatedA, err := store.MarkPublished(ctx, "evt-fencing-race", leaseA)
	require.NoError(t, err)
	require.False(t, updatedA, "FENCING VIOLATION: stale lease A overwrote new lease B")

	// Worker B's MarkPublished succeeds.
	updatedB, err := store.MarkPublished(ctx, "evt-fencing-race", leaseB)
	require.NoError(t, err)
	require.True(t, updatedB, "current lease B must own the row")
}

// insertSeedRow inserts a ClaimedEntry directly into outbox_entries with
// status='pending'. Used by the conformance suite factory to pre-populate the
// table without going through OutboxWriter (which requires a live transaction).
func insertSeedRow(t *testing.T, pool *Pool, ce rout.ClaimedEntry) {
	t.Helper()
	const insertSQL = `INSERT INTO outbox_entries
		(id, aggregate_id, aggregate_type, event_type, topic, payload, metadata, created_at, status, attempts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9)`

	e := ce.Entry
	if e.ID == "" {
		t.Fatal("insertSeedRow: entry ID must not be empty")
	}

	payload := e.Payload
	if payload == nil {
		payload = []byte(`{}`)
	}

	createdAt := e.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	var metadataJSON []byte
	if e.Metadata != nil {
		b, mErr := json.Marshal(e.Metadata)
		require.NoError(t, mErr, "metadata marshal must succeed")
		metadataJSON = b
	}

	_, err := pool.DB().Exec(context.Background(), insertSQL,
		e.ID, e.AggregateID, e.AggregateType, e.EventType,
		e.Topic, payload, metadataJSON, createdAt, ce.Attempts)
	require.NoError(t, err, "insertSeedRow must succeed for entry %s", e.ID)
}
