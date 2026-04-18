//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	rout "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
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
	migrator, err := NewMigrator(pool, MigrationsFS(), "schema_migrations_store_conformance")
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
		return NewOutboxStore(pool.DB())
	}

	outboxtest.RunStoreConformanceSuite(t, factory)
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
