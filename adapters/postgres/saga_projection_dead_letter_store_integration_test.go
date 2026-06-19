//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/projection"
)

// newDeadLetterStore builds a real PG-backed SagaProjectionDeadLetterStore on a
// fresh per-test database cloned from the migrated template (table 067 present),
// plus a TxManager for the ambient-transaction / atomicity tests.
func newDeadLetterStore(t *testing.T) (*SagaProjectionDeadLetterStore, *TxManager, *Pool) {
	t.Helper()
	pool := migratedPool(t)
	store, err := NewSagaProjectionDeadLetterStore(pool.DB())
	require.NoError(t, err)
	return store, NewTxManager(pool), pool
}

func sampleDeadLetter(seq int64) projection.DeadLetter {
	return projection.DeadLetter{
		CellID:       "orderfulfillmentcell",
		ProjectionID: "orderstatus",
		GlobalSeq:    seq,
		EventID:      fmt.Sprintf("saga-journal:%d@inst-1", seq),
		Stream:       "saga-journal",
		ErrorType:    "ERR_VALIDATION_FAILED",
		ErrorMessage: "permanent: unknown event kind",
		OccurredAt:   time.Unix(1700000000, 0).UTC(),
	}
}

// TestSagaProjectionDeadLetterStore_RecordReadBack records a poison event and
// reads every column back, proving the INSERT round-trips against real PG.
func TestSagaProjectionDeadLetterStore_RecordReadBack(t *testing.T) {
	store, _, pool := newDeadLetterStore(t)
	ctx := context.Background()
	dl := sampleDeadLetter(42)

	require.NoError(t, store.Record(ctx, dl))

	var eventID, stream, errorType, errorMessage string
	var occurredAt time.Time
	err := pool.DB().QueryRow(ctx,
		`SELECT event_id, stream, error_type, error_message, occurred_at
		   FROM saga_projection_dead_letters
		  WHERE cell_id = $1 AND projection_id = $2 AND global_seq = $3`,
		dl.CellID, dl.ProjectionID, dl.GlobalSeq).
		Scan(&eventID, &stream, &errorType, &errorMessage, &occurredAt)
	require.NoError(t, err)
	assert.Equal(t, dl.EventID, eventID)
	assert.Equal(t, dl.Stream, stream)
	assert.Equal(t, dl.ErrorType, errorType)
	assert.Equal(t, dl.ErrorMessage, errorMessage)
	assert.True(t, dl.OccurredAt.Equal(occurredAt))
}

// TestSagaProjectionDeadLetterStore_RecordIdempotent records the same poison key
// twice (a re-driven skip after a crash) and asserts ON CONFLICT DO NOTHING keeps
// exactly one row.
func TestSagaProjectionDeadLetterStore_RecordIdempotent(t *testing.T) {
	store, _, pool := newDeadLetterStore(t)
	ctx := context.Background()
	dl := sampleDeadLetter(7)

	require.NoError(t, store.Record(ctx, dl))
	require.NoError(t, store.Record(ctx, dl)) // re-driven: no-op, no error

	var count int
	require.NoError(t, pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM saga_projection_dead_letters WHERE cell_id = $1 AND projection_id = $2 AND global_seq = $3`,
		dl.CellID, dl.ProjectionID, dl.GlobalSeq).Scan(&count))
	assert.Equal(t, 1, count)
}

// TestSagaProjectionDeadLetterStore_RecordAndAdvanceAtomic is the real-transaction
// atomicity witness the unit tests cannot provide (the mem fake has no rollback):
// Record (dead-letter) + AdvanceIfOwner (checkpoint) inside ONE RunInTx must commit
// or roll back together. A regression that split them into two transactions would
// leak the dead-letter row when the surrounding tx later fails — this test detects
// exactly that ("skipped ⟺ recorded", #2110).
func TestSagaProjectionDeadLetterStore_RecordAndAdvanceAtomic(t *testing.T) {
	store, txm, pool := newDeadLetterStore(t)
	ckpt, err := NewProjectionCheckpointStore(pool.DB())
	require.NoError(t, err)
	ctx := context.Background()
	const cell, proj, owner = "orderfulfillmentcell", "orderstatus", "owner-token-1"

	// Commit path: record + advance past the poison event in one tx.
	dl := sampleDeadLetter(5)
	require.NoError(t, txm.RunInTx(ctx, func(txCtx context.Context) error {
		if e := store.Record(txCtx, dl); e != nil {
			return e
		}
		return ckpt.AdvanceIfOwner(txCtx, cell, proj, owner, dl.GlobalSeq)
	}))
	off, err := ckpt.LoadOffset(ctx, cell, proj)
	require.NoError(t, err)
	assert.Equal(t, int64(5), off, "checkpoint advanced past the poison event")
	assertDeadLetterCount(t, ctx, pool, cell, proj, 5, 1)

	// Rollback path: the surrounding tx fails AFTER record+advance — neither the
	// (new) dead-letter row nor the checkpoint advance must persist.
	dl2 := sampleDeadLetter(9)
	boom := errors.New("downstream failure after record+advance")
	runErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
		if e := store.Record(txCtx, dl2); e != nil {
			return e
		}
		if e := ckpt.AdvanceIfOwner(txCtx, cell, proj, owner, dl2.GlobalSeq); e != nil {
			return e
		}
		return boom
	})
	require.ErrorIs(t, runErr, boom)

	off, err = ckpt.LoadOffset(ctx, cell, proj)
	require.NoError(t, err)
	assert.Equal(t, int64(5), off, "rolled-back advance must not persist")
	// dl2 (global_seq 9) must NOT be present — proving the record rolled back with
	// the advance. A split-into-two-transactions regression would leave it behind.
	assertDeadLetterCount(t, ctx, pool, cell, proj, 9, 0)
}

func assertDeadLetterCount(t *testing.T, ctx context.Context, pool *Pool, cell, proj string, seq int64, want int) {
	t.Helper()
	var count int
	require.NoError(t, pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM saga_projection_dead_letters WHERE cell_id = $1 AND projection_id = $2 AND global_seq = $3`,
		cell, proj, seq).Scan(&count))
	assert.Equal(t, want, count)
}
