//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// projectionEventInsertSQL seeds one durable journal row directly. PR-01 has no production
// writer (the same-transaction journaling decorator lands in PR-02), so conformance tests
// persist rows through this raw INSERT — global_seq is auto-assigned (GENERATED ALWAYS) and
// returned so the seed can wrap the entry in the carrier the source delivers.
const projectionEventInsertSQL = `INSERT INTO projection_events
	(id, aggregate_id, aggregate_type, event_type, topic, payload, principal, created_at, occurred_at)
VALUES ($1, $2, $3, $4, $5, $6, '{}', $7, $8)
RETURNING global_seq`

// projectionEventFixture bundles the PG-backed durable source under test with a seed closure
// (inserts fresh rows and returns the *JournalEvent carriers) and a newUnseeded closure (a
// seq-0 sentinel carrier the cursor must reject as permanent).
type projectionEventFixture struct {
	pool        *Pool
	src         *PGProjectionEventSource
	seed        func(n int) []projection.ProjectionEvent
	newUnseeded func() projection.ProjectionEvent
}

func newProjectionEventJournal(t *testing.T) projectionEventFixture {
	t.Helper()
	pool := migratedPool(t)
	src, err := NewProjectionEventSource(pool.DB())
	require.NoError(t, err)

	clk := clockmock.New(time.Now())
	newEntry := func() kout.Entry {
		e, nerr := kout.NewEntry(clk, context.Background(), "ordersummary.v1", []byte(`{}`))
		require.NoError(t, nerr)
		return e
	}
	seed := func(n int) []projection.ProjectionEvent {
		events := make([]projection.ProjectionEvent, n)
		for i := 0; i < n; i++ {
			e := newEntry()
			var globalSeq int64
			row := pool.DB().QueryRow(context.Background(), projectionEventInsertSQL,
				e.ID(), e.AggregateID(), e.AggregateType(), e.EventType(), e.Topic(), e.Payload(),
				e.CreatedAt(), e.OccurredAt())
			require.NoError(t, row.Scan(&globalSeq))
			events[i] = projection.NewJournalEvent(e, globalSeq)
		}
		return events
	}
	newUnseeded := func() projection.ProjectionEvent {
		return projection.NewJournalEvent(newEntry(), 0) // seq-0 sentinel: never assigned by the journal
	}
	return projectionEventFixture{pool: pool, src: src, seed: seed, newUnseeded: newUnseeded}
}

// TestPGProjectionEventSource_ReplayConformance enrolls the concrete *PGProjectionEventSource
// in the shared ReplaySource conformance harness against a real projection_events journal.
func TestPGProjectionEventSource_ReplayConformance(t *testing.T) {
	f := newProjectionEventJournal(t)
	projectiontest.RunReplaySourceConformance(t, f.src, f.seed)
}

// TestPGProjectionEventSource_CursorConformance enrolls the concrete *PGProjectionEventSource
// in the shared Cursor conformance harness, verifying the four cursor.go position invariants.
func TestPGProjectionEventSource_CursorConformance(t *testing.T) {
	f := newProjectionEventJournal(t)
	projectiontest.RunCursorConformance(t, f.src, f.seed, f.newUnseeded)
}

// TestPGProjectionEventSource_ColdStart verifies Head returns 0 on an empty journal and the
// cursor returns a permanent error for the unseeded sentinel.
func TestPGProjectionEventSource_ColdStart(t *testing.T) {
	f := newProjectionEventJournal(t)
	head, err := f.src.Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), head, "empty journal Head must be 0 (cold-start sentinel)")

	_, err = f.src.Position(f.newUnseeded())
	require.Error(t, err)
	var permErr *kout.PermanentError
	assert.True(t, errors.As(err, &permErr),
		"cursor must return a permanent error for the unseeded sentinel")
}

// TestPGProjectionEventSource_RepoReadiness_Conformance runs the single-source RepoProber
// harness against PGProjectionEventSource (CELL-REPO-READYZ-PROBE-01 enrollment):
//   - healthy: RepoReady returns nil when projection_events is present.
//   - broken: RepoReady errors when projection_events is dropped — a failure domain the
//     pool-level postgres_ready ping cannot detect.
func TestPGProjectionEventSource_RepoReadiness_Conformance(t *testing.T) {
	ctx := context.Background()

	healthyPool := migratedPool(t)
	healthy, err := NewProjectionEventSource(healthyPool.DB())
	require.NoError(t, err)

	brokenPool := migratedPool(t)
	_, execErr := brokenPool.DB().Exec(ctx, `DROP TABLE IF EXISTS projection_events CASCADE`)
	require.NoError(t, execErr, "drop projection_events for broken scenario")
	broken, err := NewProjectionEventSource(brokenPool.DB())
	require.NoError(t, err)

	celltest.RunRepoReadinessConformance(t, "projection-journal-pg", healthy, broken)
}
