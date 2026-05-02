//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	rout "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
