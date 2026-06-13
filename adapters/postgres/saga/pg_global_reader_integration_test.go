//go:build integration

package saga_test

// This file contains:
//   A3 — GlobalReader conformance enrollment for PGJournal
//        (SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01)
//   A4 — PG-backed SagaJournalSource replay source conformance
//
// RunCursorConformance is NOT run here — Position is backend-agnostic kernel
// code already enrolled via the in-memory journal in
// kernel/saga/sagaprojection/source_test.go. Enrolling it again against PG
// would test the same kernel logic, not the adapter; the deliberate scope
// call avoids spurious duplication.

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/postgres/saga"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
)

// ---------------------------------------------------------------------------
// A3: GlobalReader conformance enrollment
// ---------------------------------------------------------------------------

// TestPGSagaJournal_GlobalReaderConformance enrolls PGJournal in the canonical
// RunGlobalReaderConformance suite (SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01).
// Each conformance sub-test gets a fresh per-test database via sharedPG.NewPerTestPool.
func TestPGSagaJournal_GlobalReaderConformance(t *testing.T) {
	factory := func(t *testing.T) (journal.Journal, *clockmock.FakeClock, func()) {
		t.Helper()
		pool := sharedPG.NewPerTestPool(t)
		clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
		j, err := saga.NewJournal(pool.DB(), clk)
		require.NoError(t, err)
		return j, clk, func() {}
	}

	sagajournaltest.RunGlobalReaderConformance(t, factory)
}

// ---------------------------------------------------------------------------
// A4: PG-backed SagaJournalSource replay source conformance
// ---------------------------------------------------------------------------

// TestPGSagaJournalSource_ReplayConformance enrolls the PG-backed
// SagaJournalSource in the canonical RunReplaySourceConformance suite.
//
// RunCursorConformance is intentionally omitted — see file-level comment above.
func TestPGSagaJournalSource_ReplayConformance(t *testing.T) {
	pool := sharedPG.NewPerTestPool(t)
	clk := clockmock.New(time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	pgJ, err := saga.NewJournal(pool.DB(), clk)
	require.NoError(t, err)
	// Hold as journal.Journal so the interface assertion below compiles; PGJournal
	// must implement GlobalReader (asserted by the var _ compile guard in global_reader.go).
	var j journal.Journal = pgJ

	gr, ok := j.(journal.GlobalReader)
	require.True(t, ok, "PGJournal must implement journal.GlobalReader")

	src, err := sagaprojection.NewSagaJournalSource(gr)
	require.NoError(t, err)

	seed := func(n int) []projection.ProjectionEvent {
		return pgSeedGlobalEvents(t, pgJ, clk, src, n)
	}

	projectiontest.RunReplaySourceConformance(t, src, seed)
}

// ---------------------------------------------------------------------------
// seed helper for PG tests
// ---------------------------------------------------------------------------

// pgSeedCounter provides unique instance IDs across multiple pgSeedGlobalEvents
// calls in the same test binary. Using a separate counter from the mem test
// helper avoids any theoretical collision on the shared PG template database
// (though in practice each test gets its own per-test database, the counter
// still provides a clean discipline).
var pgSeedCounter atomic.Int64

// pgSeedGlobalEvents appends n saga journal events to j via the PG backend
// and returns them as []projection.ProjectionEvent via SagaJournalSource.Replay.
//
// The pattern mirrors kernel/saga/sagaprojection/source_test.go:seedGlobalEvents:
// create a fresh instance per call, claim it, append n KindStepStarted events,
// then Replay from the recorded head position to collect the newly appended carriers.
func pgSeedGlobalEvents(
	t *testing.T,
	j journal.Journal,
	clk *clockmock.FakeClock,
	src *sagaprojection.SagaJournalSource,
	n int,
) []projection.ProjectionEvent {
	t.Helper()

	// Unique instance ID per seed call to avoid ErrSagaDuplicateInstance across
	// multiple seed calls on the same journal.
	instID := "pg-seed-inst-" + strconv.Itoa(int(pgSeedCounter.Add(1)))
	inst := sagajournaltest.NewInstanceFixture(t, instID, clk.Now())

	ctx := context.Background()
	require.NoError(t, j.Enqueue(ctx, inst), "Enqueue %s", instID)

	_, leaseID, err := j.ClaimPending(ctx, 100, time.Hour)
	require.NoError(t, err, "ClaimPending %s", instID)
	require.NotEmpty(t, leaseID, "leaseID must not be empty for %s", instID)

	// Record head before appending so Replay collects only the new events.
	beforeHead, err := src.Head(ctx)
	require.NoError(t, err, "Head before seed")

	for range n {
		_, err := j.Append(ctx, inst.ID, leaseID, journal.Event{
			Kind:     journal.KindStepStarted,
			StepName: "step-one",
		})
		require.NoError(t, err, "Append to %s", instID)
	}

	var results []projection.ProjectionEvent
	err = src.Replay(ctx, beforeHead, func(e projection.ProjectionEvent) error {
		results = append(results, e)
		return nil
	})
	require.NoError(t, err, "seed Replay")
	require.GreaterOrEqual(t, len(results), n,
		"seed produced %d events via Replay, want at least %d", len(results), n)

	return results[:n]
}
