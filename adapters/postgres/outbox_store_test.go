package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testLease is a fixed lease ID used across unit-test mark calls — the
// mockDBTX returns RowsAffected from a fixture, so the value carried by the
// SQL parameter is what we assert. (PG-side fencing semantics are covered by
// the integration suite.)
var testLease = uuid.NewString()

const (
	outboxTestNeg10s    = -10 * time.Second
	outboxTestNeg72h    = -72 * time.Hour
	outboxTestNeg30Days = -30 * 24 * time.Hour
)

// ---------------------------------------------------------------------------
// MarkPublished unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_MarkPublished_Updated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	updated, err := store.MarkPublished(context.Background(), "e-1", testLease)
	require.NoError(t, err)
	assert.True(t, updated, "RowsAffected=1 should return updated=true")

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "published_at = now()", "should set published_at")
	assert.Contains(t, ec.sql, "status = $3", "should include optimistic lock on status")
	assert.Contains(t, ec.sql, "lease_id = $4", "should fence on lease_id")
	// args: $1=statusPublished, $2=id, $3=statusClaiming, $4=leaseID
	assert.Equal(t, statusPublished, ec.args[0])
	assert.Equal(t, "e-1", ec.args[1])
	assert.Equal(t, statusClaiming, ec.args[2])
	assert.Equal(t, testLease, ec.args[3])
}

func TestPGOutboxStore_MarkPublished_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db, clock.Real())

	updated, err := store.MarkPublished(context.Background(), "e-reclaimed", testLease)
	require.NoError(t, err)
	assert.False(t, updated, "RowsAffected=0 should return updated=false (lease was lost)")
}

func TestPGOutboxStore_MarkPublished_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.MarkPublished(context.Background(), "e-fail", testLease)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MarkPublished failed")
}

// ---------------------------------------------------------------------------
// MarkRetry unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_MarkRetry_Updated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	nextRetry := time.Now().Add(testtime.D10s)
	updated, err := store.MarkRetry(context.Background(), "e-1", testLease, 2, nextRetry, "transient error")
	require.NoError(t, err)
	assert.True(t, updated)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	// Verify SQL structure
	assert.Contains(t, ec.sql, "next_retry_at = now() +", "should set next_retry_at")
	assert.Contains(t, ec.sql, "last_error = $4", "should set last_error")
	assert.Contains(t, ec.sql, "lease_id = $7", "should fence on lease_id")
	// args: $1=pending, $2=attempts, $3=interval, $4=errMsg, $5=id, $6=claiming, $7=leaseID
	assert.Equal(t, statusPending, ec.args[0])
	assert.Equal(t, 2, ec.args[1])
	assert.Equal(t, "e-1", ec.args[4])
	assert.Equal(t, statusClaiming, ec.args[5])
	assert.Equal(t, testLease, ec.args[6])

	// Interval arg ($3) should be a non-empty string with microseconds
	intervalArg, ok := ec.args[2].(string)
	require.True(t, ok, "interval arg should be a string")
	assert.Contains(t, intervalArg, "microseconds")
}

func TestPGOutboxStore_MarkRetry_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db, clock.Real())

	updated, err := store.MarkRetry(context.Background(), "e-gone", testLease, 1, time.Now().Add(testtime.D5s), "err")
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestPGOutboxStore_MarkRetry_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec retry failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.MarkRetry(context.Background(), "e-fail", testLease, 1, time.Now().Add(testtime.D5s), "err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MarkRetry failed")
}

func TestPGOutboxStore_MarkRetry_SanitizesError(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.MarkRetry(context.Background(), "e-1", testLease, 1,
		time.Now().Add(testtime.D5s),
		"dial tcp: password=secret123 host=db.internal")
	require.NoError(t, err)

	ec := db.execCalls[0]
	errArg, _ := ec.args[3].(string)
	assert.NotContains(t, errArg, "secret123", "sensitive data should be redacted")
	assert.Contains(t, errArg, "password=<REDACTED>")
}

func TestPGOutboxStore_MarkRetry_PastNextRetry_UsesZeroDelay(t *testing.T) {
	// nextRetryAt in the past → delay < 0 → clamped to 0
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	pastTime := time.Now().Add(outboxTestNeg10s)
	_, err := store.MarkRetry(context.Background(), "e-1", testLease, 1, pastTime, "err")
	require.NoError(t, err)

	ec := db.execCalls[0]
	intervalArg, ok := ec.args[2].(string)
	require.True(t, ok)
	assert.Equal(t, "0 microseconds", intervalArg, "past nextRetryAt should yield 0 interval")
}

// ---------------------------------------------------------------------------
// MarkDead unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_MarkDead_Updated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	updated, err := store.MarkDead(context.Background(), "e-1", testLease, 5, "permanent failure")
	require.NoError(t, err)
	assert.True(t, updated)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "dead_at = now()", "should set dead_at")
	assert.Contains(t, ec.sql, "lease_id = $6", "should fence on lease_id")
	// args: $1=dead, $2=attempts, $3=errMsg, $4=id, $5=claiming, $6=leaseID
	assert.Equal(t, statusDead, ec.args[0])
	assert.Equal(t, 5, ec.args[1])
	assert.Equal(t, "e-1", ec.args[3])
	assert.Equal(t, statusClaiming, ec.args[4])
	assert.Equal(t, testLease, ec.args[5])
}

func TestPGOutboxStore_MarkDead_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db, clock.Real())

	updated, err := store.MarkDead(context.Background(), "e-gone", testLease, 5, "err")
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestPGOutboxStore_MarkDead_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec dead failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.MarkDead(context.Background(), "e-fail", testLease, 5, "err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MarkDead failed")
}

func TestPGOutboxStore_MarkDead_SanitizesError(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.MarkDead(context.Background(), "e-1", testLease, 5, "token=abc123 failed")
	require.NoError(t, err)

	ec := db.execCalls[0]
	errArg, _ := ec.args[2].(string)
	assert.NotContains(t, errArg, "abc123")
	assert.Contains(t, errArg, "token=<REDACTED>")
}

// ---------------------------------------------------------------------------
// ReclaimStale unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_ReclaimStale_ReturnsCount(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 3"),
	}
	store := NewOutboxStore(db, clock.Real())

	const callerBatch = 1234

	count, err := store.ReclaimStale(context.Background(),
		testtime.D60s, 5, testtime.D5s, testtime.D5min, callerBatch)
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "attempts = picked.attempts + 1",
		"must increment attempts using the CTE-snapshotted value")
	assert.Contains(t, ec.sql, "CASE WHEN picked.attempts + 1 >= $2",
		"must use CASE expression keyed on the CTE-snapshotted attempts for dead/pending branch")
	assert.Contains(t, ec.sql, "LIMIT $8", "must cap batch size to avoid long transactions")
	assert.Contains(t, ec.sql, "lease_id = CASE", "reclaim must clear lease_id on back-to-pending branch")
	assert.Contains(t, ec.sql, "FOR UPDATE SKIP LOCKED",
		"CTE picker must use SKIP LOCKED to avoid contending with mark CAS transactions")
	assert.Contains(t, ec.sql, "o.status = $6",
		"outer UPDATE must re-assert status (write-time CAS prevents regression of rows that left claiming)")
	assert.Contains(t, ec.sql, "o.lease_id = picked.lease_id",
		"outer UPDATE must re-assert lease_id matches the picked snapshot (write-time fencing)")

	// args: $1=claimTTLInterval, $2=maxAttempts, $3=dead, $4=pending,
	//       $5=baseDelayMicros, $6=claiming, $7=maxDelayMicros, $8=callerBatchSize
	assert.Equal(t, 5, ec.args[1], "maxAttempts")
	assert.Equal(t, statusDead, ec.args[2], "dead status")
	assert.Equal(t, statusPending, ec.args[3], "pending status")
	assert.Equal(t, statusClaiming, ec.args[5], "claiming status for WHERE clause")
	assert.Equal(t, callerBatch, ec.args[7], "ReclaimStale must pass through the caller's batchSize")

	// claimTTL interval text
	ttlArg, ok := ec.args[0].(string)
	require.True(t, ok)
	assert.Contains(t, ttlArg, "microseconds")
}

func TestPGOutboxStore_ReclaimStale_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec reclaim failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.ReclaimStale(context.Background(),
		testtime.D60s, 5, testtime.D5s, testtime.D5min, 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReclaimStale failed")
}

func TestPGOutboxStore_ReclaimStale_ZeroCount(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db, clock.Real())

	count, err := store.ReclaimStale(context.Background(),
		testtime.D60s, 5, testtime.D5s, testtime.D5min, 1000)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// CleanupPublished unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_CleanupPublished_ReturnsCount(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("DELETE 5"),
	}
	store := NewOutboxStore(db, clock.Real())

	cutoff := time.Now().Add(outboxTestNeg72h)
	deleted, err := store.CleanupPublished(context.Background(), cutoff, 1000)
	require.NoError(t, err)
	assert.Equal(t, 5, deleted)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "published_at", "should filter by published_at")
	assert.Contains(t, ec.sql, "LIMIT", "should have LIMIT for batched execution")
	assert.Equal(t, statusPublished, ec.args[0])
}

func TestPGOutboxStore_CleanupPublished_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec cleanup failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.CleanupPublished(context.Background(), time.Now(), 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CleanupPublished failed")
}

// ---------------------------------------------------------------------------
// CleanupDead unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_CleanupDead_ReturnsCount(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("DELETE 2"),
	}
	store := NewOutboxStore(db, clock.Real())

	cutoff := time.Now().Add(outboxTestNeg30Days)
	deleted, err := store.CleanupDead(context.Background(), cutoff, 1000)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "dead_at", "should filter by dead_at")
	assert.Contains(t, ec.sql, "LIMIT", "should have LIMIT for batched execution")
	assert.Equal(t, statusDead, ec.args[0])
}

func TestPGOutboxStore_CleanupDead_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec dead cleanup failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.CleanupDead(context.Background(), time.Now(), 1000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CleanupDead failed")
}

// ---------------------------------------------------------------------------
// ClaimPending unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_ClaimPending_Empty(t *testing.T) {
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
	}
	store := NewOutboxStore(db, clock.Real())

	entries, err := store.ClaimPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestPGOutboxStore_ClaimPending_ReturnsEntries(t *testing.T) {
	e1 := makeRelayEntry("e-1", "order.created", 0)
	e2 := makeRelayEntry("e-2", "order.updated", 1)

	db := &mockDBTX{
		queryRows: &mockRows{entries: []mockRowData{
			makeMockRowData(e1),
			makeMockRowData(e2),
		}},
	}
	store := NewOutboxStore(db, clock.Real())

	entries, err := store.ClaimPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "e-1", entries[0].ID)
	assert.Equal(t, "e-2", entries[1].ID)
	assert.Equal(t, 0, entries[0].Attempts)
	assert.Equal(t, 1, entries[1].Attempts)
}

func TestPGOutboxStore_ClaimPending_SQLContainsSkipLocked(t *testing.T) {
	db := &mockDBTX{queryRows: &mockRows{entries: nil}}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.ClaimPending(context.Background(), 5)
	require.NoError(t, err)

	require.NotEmpty(t, db.queryCalls)
	sql := db.queryCalls[0].sql
	assert.Contains(t, sql, "WITH picked AS MATERIALIZED")
	assert.Contains(t, sql, "FOR UPDATE SKIP LOCKED")
	assert.Contains(t, sql, "next_retry_at IS NULL OR next_retry_at <= now()")
	assert.Contains(t, sql, "ORDER BY next_retry_at NULLS FIRST, created_at, id")
	assert.Contains(t, sql, "FROM updated")
	assert.Contains(t, sql, "ORDER BY picked_next_retry_at NULLS FIRST, picked_created_at, id")

	args := db.queryCalls[0].args
	assert.Equal(t, statusClaiming, args[0])
	assert.Equal(t, statusPending, args[1])
	assert.Equal(t, 5, args[2])
}

func TestPGOutboxStore_ClaimPending_MetadataNull(t *testing.T) {
	e := makeRelayEntry("e-meta", "order.created", 0)
	// Simulate NULL metadata (JSON null bytes) and NULL observability.
	row := mockRowData{
		values: []any{
			e.ID, e.AggregateID, e.AggregateType, e.EventType,
			e.Topic, e.Payload,
			[]byte("null"), // JSON null metadata
			e.CreatedAt, e.Attempts,
			[]byte(nil), // NULL observability
			uuid.New(),  // lease_id
		},
	}
	db := &mockDBTX{queryRows: &mockRows{entries: []mockRowData{row}}}
	store := NewOutboxStore(db, clock.Real())

	entries, err := store.ClaimPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// null bytes → len > 0 but json.Unmarshal("null") sets metadata to nil
	assert.Nil(t, entries[0].Metadata)
	assert.True(t, entries[0].Observability.IsZero())
}

func TestPGOutboxStore_ClaimPending_BeginError(t *testing.T) {
	db := &mockDBTX{beginErr: errors.New("connection refused")}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.ClaimPending(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin tx")
}

func TestPGOutboxStore_ClaimPending_CommitError(t *testing.T) {
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
		commitErr: errors.New("commit failed"),
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.ClaimPending(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit failed")
}

func TestPGOutboxStore_ClaimPending_ScanError(t *testing.T) {
	// Row with wrong number of columns triggers scan error.
	db := &mockDBTX{
		queryRows: &mockRows{entries: []mockRowData{
			{values: []any{"too-few"}},
		}},
	}
	store := NewOutboxStore(db, clock.Real())

	_, err := store.ClaimPending(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ClaimPending scan failed")
}

func TestPGOutboxStore_ClaimPending_InvalidMetadataJSON(t *testing.T) {
	e := makeRelayEntry("e-bad-meta", "order.created", 0)
	row := mockRowData{
		values: []any{
			e.ID, e.AggregateID, e.AggregateType, e.EventType,
			e.Topic, e.Payload,
			[]byte(`{invalid-json`),
			e.CreatedAt, e.Attempts,
			[]byte(nil), // NULL observability
			uuid.New(),  // lease_id
		},
	}
	db := &mockDBTX{queryRows: &mockRows{entries: []mockRowData{row}}}
	store := NewOutboxStore(db, clock.Real())

	// Invalid JSON must not fail ClaimPending — entry still returned, metadata nil.
	entries, err := store.ClaimPending(context.Background(), 10)
	require.NoError(t, err, "invalid metadata JSON must not fail ClaimPending")
	require.Len(t, entries, 1)
	assert.Nil(t, entries[0].Metadata)
}

func TestPGOutboxStore_ClaimPending_RowsIterError(t *testing.T) {
	iterErr := errors.New("rows iteration error")
	db := &mockDBTXIterErr{iterErr: iterErr}
	store := &PGOutboxStore{db: db}

	_, err := store.ClaimPending(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ClaimPending rows iteration failed")
}
