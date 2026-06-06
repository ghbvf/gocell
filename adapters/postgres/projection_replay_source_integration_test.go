//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// projectionJournalFixture bundles the PG-backed ReplaySource + Cursor under test
// with a seed closure (persists fresh entries through the real OutboxWriter inside
// a transaction, as a producer would) and a newUnseeded closure (a fresh entry
// never written to the journal — for the cursor permanent-error path).
type projectionJournalFixture struct {
	pool        *Pool
	src         *PGProjectionReplaySource
	cursor      *PGProjectionCursor
	seed        func(n int) []projection.ProjectionEvent
	newUnseeded func() projection.ProjectionEvent
}

// newProjectionJournal builds the fixture on a fresh per-test database cloned from
// the migrated template (table 047 seq column present).
func newProjectionJournal(t *testing.T) projectionJournalFixture {
	t.Helper()
	pool := migratedPool(t)
	src, err := NewProjectionReplaySource(pool.DB())
	require.NoError(t, err)
	cursor, err := NewProjectionCursor(src)
	require.NoError(t, err)

	clk := clockmock.New(time.Now())
	txm := NewTxManager(pool)
	writer := NewOutboxWriter(clk)

	newEntry := func() kout.Entry {
		e, nerr := kout.NewEntry(clk, context.Background(), "ordersummary.v1", []byte(`{}`))
		require.NoError(t, nerr)
		return e
	}
	seed := func(n int) []projection.ProjectionEvent {
		koutEntries := make([]kout.Entry, n)
		runErr := txm.RunInTx(context.Background(), func(txCtx context.Context) error {
			for i := 0; i < n; i++ {
				e := newEntry()
				if werr := writer.Write(txCtx, e); werr != nil {
					return werr
				}
				koutEntries[i] = e
			}
			return nil
		})
		require.NoError(t, runErr)
		events := make([]projection.ProjectionEvent, n)
		for i, e := range koutEntries {
			events[i] = e
		}
		return events
	}
	newUnseeded := func() projection.ProjectionEvent { return newEntry() }
	return projectionJournalFixture{pool: pool, src: src, cursor: cursor, seed: seed, newUnseeded: newUnseeded}
}

// TestPGProjectionReplaySource_Conformance enrolls the concrete
// *PGProjectionReplaySource in the shared ReplaySource conformance harness
// (PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01) against a real PG journal.
func TestPGProjectionReplaySource_Conformance(t *testing.T) {
	f := newProjectionJournal(t)
	projectiontest.RunReplaySourceConformance(t, f.src, f.seed)
}

// TestPGProjectionCursor_Conformance enrolls the concrete *PGProjectionCursor in
// the shared Cursor conformance harness (PROJECTION-CURSOR-CONFORMANCE-ENROLL-01)
// against a real PG journal, verifying the four cursor.go position invariants.
func TestPGProjectionCursor_Conformance(t *testing.T) {
	f := newProjectionJournal(t)
	projectiontest.RunCursorConformance(t, f.cursor, f.seed, f.newUnseeded)
}

// TestPGProjectionJournal_ColdStart verifies Head returns 0 on an empty journal
// and the cursor returns a permanent error for an entry that was never written.
func TestPGProjectionJournal_ColdStart(t *testing.T) {
	f := newProjectionJournal(t)
	head, err := f.src.Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), head, "empty journal Head must be 0 (cold-start sentinel)")

	_, err = f.cursor.Position(f.newUnseeded())
	require.Error(t, err)
	var permErr *kout.PermanentError
	assert.True(t, errors.As(err, &permErr),
		"cursor must return a permanent error for an entry absent from the journal")
}

// TestPGProjectionJournal_GapAfterCleanup is the gap-allowed / retention-boundary
// proof: after a middle row is deleted (as CleanupPublished/CleanupDead would),
// Replay delivers the survivors in order, the cursor still resolves them, and the
// deleted entry resolves to a permanent error — never a transient one.
func TestPGProjectionJournal_GapAfterCleanup(t *testing.T) {
	f := newProjectionJournal(t)

	entries := f.seed(3)
	deleted := entries[1]

	// Delete the middle entry, creating a position gap (e.g. 1, _, 3) — as the
	// relay's CleanupPublished/CleanupDead would after retention.
	_, err := f.pool.DB().Exec(context.Background(),
		`DELETE FROM outbox_entries WHERE id = $1`, deleted.EventID())
	require.NoError(t, err)

	// Replay(0) delivers exactly the two survivors, in ascending order.
	var got []string
	require.NoError(t, f.src.Replay(context.Background(), 0, func(e projection.ProjectionEvent) error {
		got = append(got, e.EventID())
		return nil
	}))
	assert.Equal(t, []string{entries[0].EventID(), entries[2].EventID()}, got,
		"Replay must deliver survivors in order, skipping the cleaned-up gap")

	// Survivors still resolve to strictly increasing positions (gap tolerated).
	p0, err := f.cursor.Position(entries[0])
	require.NoError(t, err)
	p2, err := f.cursor.Position(entries[2])
	require.NoError(t, err)
	assert.Greater(t, p2, p0, "survivor positions remain monotonically increasing across the gap")

	// The deleted entry is unresolvable → permanent error.
	_, err = f.cursor.Position(deleted)
	require.Error(t, err)
	var permErr *kout.PermanentError
	assert.True(t, errors.As(err, &permErr),
		"a cleaned-up entry must resolve to a permanent error, not a transient retry")
}
