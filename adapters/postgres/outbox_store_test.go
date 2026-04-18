package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// MarkPublished unit tests
// ---------------------------------------------------------------------------

func TestPGOutboxStore_MarkPublished_Updated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db)

	updated, err := store.MarkPublished(context.Background(), "e-1")
	require.NoError(t, err)
	assert.True(t, updated, "RowsAffected=1 should return updated=true")

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "published_at = now()", "should set published_at")
	assert.Contains(t, ec.sql, "status = $3", "should include optimistic lock on status")
	// args: $1=statusPublished, $2=id, $3=statusClaiming
	assert.Equal(t, statusPublished, ec.args[0])
	assert.Equal(t, "e-1", ec.args[1])
	assert.Equal(t, statusClaiming, ec.args[2])
}

func TestPGOutboxStore_MarkPublished_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db)

	updated, err := store.MarkPublished(context.Background(), "e-reclaimed")
	require.NoError(t, err)
	assert.False(t, updated, "RowsAffected=0 should return updated=false (entry was reclaimed)")
}

func TestPGOutboxStore_MarkPublished_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec failed"),
	}
	store := NewOutboxStore(db)

	_, err := store.MarkPublished(context.Background(), "e-fail")
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
	store := NewOutboxStore(db)

	nextRetry := time.Now().Add(10 * time.Second)
	updated, err := store.MarkRetry(context.Background(), "e-1", 2, nextRetry, "transient error")
	require.NoError(t, err)
	assert.True(t, updated)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	// Verify SQL structure
	assert.Contains(t, ec.sql, "next_retry_at = now() +", "should set next_retry_at")
	assert.Contains(t, ec.sql, "last_error = $4", "should set last_error")
	// args: $1=pending, $2=attempts, $3=interval, $4=errMsg, $5=id, $6=claiming
	assert.Equal(t, statusPending, ec.args[0])
	assert.Equal(t, 2, ec.args[1])
	assert.Equal(t, "e-1", ec.args[4])
	assert.Equal(t, statusClaiming, ec.args[5])

	// Interval arg ($3) should be a non-empty string with microseconds
	intervalArg, ok := ec.args[2].(string)
	require.True(t, ok, "interval arg should be a string")
	assert.Contains(t, intervalArg, "microseconds")
}

func TestPGOutboxStore_MarkRetry_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db)

	updated, err := store.MarkRetry(context.Background(), "e-gone", 1, time.Now().Add(5*time.Second), "err")
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestPGOutboxStore_MarkRetry_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec retry failed"),
	}
	store := NewOutboxStore(db)

	_, err := store.MarkRetry(context.Background(), "e-fail", 1, time.Now().Add(5*time.Second), "err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MarkRetry failed")
}

func TestPGOutboxStore_MarkRetry_SanitizesError(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db)

	_, err := store.MarkRetry(context.Background(), "e-1", 1,
		time.Now().Add(5*time.Second),
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
	store := NewOutboxStore(db)

	pastTime := time.Now().Add(-10 * time.Second)
	_, err := store.MarkRetry(context.Background(), "e-1", 1, pastTime, "err")
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
	store := NewOutboxStore(db)

	updated, err := store.MarkDead(context.Background(), "e-1", 5, "permanent failure")
	require.NoError(t, err)
	assert.True(t, updated)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "dead_at = now()", "should set dead_at")
	// args: $1=dead, $2=attempts, $3=errMsg, $4=id, $5=claiming
	assert.Equal(t, statusDead, ec.args[0])
	assert.Equal(t, 5, ec.args[1])
	assert.Equal(t, "e-1", ec.args[3])
	assert.Equal(t, statusClaiming, ec.args[4])
}

func TestPGOutboxStore_MarkDead_NotUpdated(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db)

	updated, err := store.MarkDead(context.Background(), "e-gone", 5, "err")
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestPGOutboxStore_MarkDead_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec dead failed"),
	}
	store := NewOutboxStore(db)

	_, err := store.MarkDead(context.Background(), "e-fail", 5, "err")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MarkDead failed")
}

func TestPGOutboxStore_MarkDead_SanitizesError(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 1"),
	}
	store := NewOutboxStore(db)

	_, err := store.MarkDead(context.Background(), "e-1", 5, "token=abc123 failed")
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
	store := NewOutboxStore(db)

	count, err := store.ReclaimStale(context.Background(),
		60*time.Second, 5, 5*time.Second, 5*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	require.Len(t, db.execCalls, 1)
	ec := db.execCalls[0]
	assert.Contains(t, ec.sql, "attempts = attempts + 1", "must increment attempts")
	assert.Contains(t, ec.sql, "CASE WHEN attempts + 1 >= $2", "must use CASE expression for dead/pending")

	// args: $1=claimTTLInterval, $2=maxAttempts, $3=dead, $4=pending,
	//       $5=baseDelayMicros, $6=claiming, $7=maxDelayMicros
	assert.Equal(t, 5, ec.args[1], "maxAttempts")
	assert.Equal(t, statusDead, ec.args[2], "dead status")
	assert.Equal(t, statusPending, ec.args[3], "pending status")
	assert.Equal(t, statusClaiming, ec.args[5], "claiming status for WHERE clause")

	// claimTTL interval text
	ttlArg, ok := ec.args[0].(string)
	require.True(t, ok)
	assert.Contains(t, ttlArg, "microseconds")
}

func TestPGOutboxStore_ReclaimStale_ExecError(t *testing.T) {
	db := &mockDBTX{
		execErr: errors.New("exec reclaim failed"),
	}
	store := NewOutboxStore(db)

	_, err := store.ReclaimStale(context.Background(),
		60*time.Second, 5, 5*time.Second, 5*time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ReclaimStale failed")
}

func TestPGOutboxStore_ReclaimStale_ZeroCount(t *testing.T) {
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	store := NewOutboxStore(db)

	count, err := store.ReclaimStale(context.Background(),
		60*time.Second, 5, 5*time.Second, 5*time.Minute)
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
	store := NewOutboxStore(db)

	cutoff := time.Now().Add(-72 * time.Hour)
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
	store := NewOutboxStore(db)

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
	store := NewOutboxStore(db)

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
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
	store := NewOutboxStore(db)

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
	store := NewOutboxStore(db)

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
	store := NewOutboxStore(db)

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
	store := NewOutboxStore(db)

	_, err := store.ClaimPending(context.Background(), 5)
	require.NoError(t, err)

	require.NotEmpty(t, db.queryCalls)
	sql := db.queryCalls[0].sql
	assert.Contains(t, sql, "FOR UPDATE SKIP LOCKED")
	assert.Contains(t, sql, "next_retry_at IS NULL OR next_retry_at <= now()")

	args := db.queryCalls[0].args
	assert.Equal(t, statusClaiming, args[0])
	assert.Equal(t, statusPending, args[1])
	assert.Equal(t, 5, args[2])
}

func TestPGOutboxStore_ClaimPending_MetadataNull(t *testing.T) {
	e := makeRelayEntry("e-meta", "order.created", 0)
	// Simulate NULL metadata (JSON null bytes)
	row := mockRowData{
		values: []any{
			e.ID, e.AggregateID, e.AggregateType, e.EventType,
			e.Topic, e.Payload,
			[]byte("null"), // JSON null
			e.CreatedAt, e.Attempts,
		},
	}
	db := &mockDBTX{queryRows: &mockRows{entries: []mockRowData{row}}}
	store := NewOutboxStore(db)

	entries, err := store.ClaimPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	// null bytes → len > 0 but json.Unmarshal("null") sets metadata to nil
	assert.Nil(t, entries[0].Metadata)
}

func TestPGOutboxStore_ClaimPending_BeginError(t *testing.T) {
	db := &mockDBTX{beginErr: errors.New("connection refused")}
	store := NewOutboxStore(db)

	_, err := store.ClaimPending(context.Background(), 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin tx")
}

func TestPGOutboxStore_ClaimPending_CommitError(t *testing.T) {
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
		commitErr: errors.New("commit failed"),
	}
	store := NewOutboxStore(db)

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
	store := NewOutboxStore(db)

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
		},
	}
	db := &mockDBTX{queryRows: &mockRows{entries: []mockRowData{row}}}
	store := NewOutboxStore(db)

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
