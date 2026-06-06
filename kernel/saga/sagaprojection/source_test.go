// Package sagaprojection provides the bridge from kernel/saga/journal.GlobalReader
// to the projection harness (ReplaySource + Cursor + cellvocab.ProjectionEvent).
//
// PR-03 of EPIC #1609 (#1627).
package sagaprojection_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// newMemJournalFactory returns a sagajournaltest.Factory backed by the in-memory
// journal implementation. The factory is the standard seed shape for GlobalReader
// conformance suites.
func newMemJournalFactory() sagajournaltest.Factory {
	return func(t *testing.T) (journal.Journal, *clockmock.FakeClock, func()) {
		t.Helper()
		clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		j, err := journal.NewMemJournal(clk)
		if err != nil {
			t.Fatalf("NewMemJournal: %v", err)
		}
		return j, clk, func() {}
	}
}

// ---------------------------------------------------------------------------
// Conformance enrolment: ReplaySource + Cursor
// ---------------------------------------------------------------------------

// TestSagaJournalSource_ReplaySourceConformance enrolls SagaJournalSource in the
// canonical RunReplaySourceConformance suite (SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01
// sister rule — projection side).
func TestSagaJournalSource_ReplaySourceConformance(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	seed := func(n int) []projection.ProjectionEvent {
		return seedGlobalEvents(t, j, clk, n)
	}

	projectiontest.RunReplaySourceConformance(t, src, seed)
}

// TestSagaJournalSource_CursorConformance enrolls SagaJournalSource in the
// canonical RunCursorConformance suite.
func TestSagaJournalSource_CursorConformance(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	seed := func(n int) []projection.ProjectionEvent {
		return seedGlobalEvents(t, j, clk, n)
	}

	// newUnseeded returns a saga projection event never appended to this journal;
	// the cursor must return a permanent error for an unknown event.
	projectiontest.RunCursorConformance(t, src, seed, sagaprojection.NewUnseededEventForTest)
}

// ---------------------------------------------------------------------------
// Carrier identity: RestoreContext installs SystemPrincipalActor
// ---------------------------------------------------------------------------

// TestSagaProjectionEvent_RestoreContext_InstallsSystemPrincipal asserts that a
// saga journal carrier's RestoreContext always overwrites the ambient principal
// with SystemPrincipalActor ("system"), regardless of any prior ctx actor.
//
// This is the saga-journal path complement to TestRebuild_EventPrincipalWins in
// the projection package (the outbox path), documented in ADR #1609 §5.
func TestSagaProjectionEvent_RestoreContext_InstallsSystemPrincipal(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	// Append one journal event so we have something to Replay.
	events := seedGlobalEvents(t, j, clk, 1)
	if len(events) != 1 {
		t.Fatalf("seed returned %d events, want 1", len(events))
	}

	// Replay from offset 0 and capture the ctx actor after RestoreContext.
	var capturedActor string
	ambientCtx := ctxkeys.WithActorID(context.Background(), "ambient-admin")
	err := src.Replay(ambientCtx, 0, func(e projection.ProjectionEvent) error {
		restored := e.RestoreContext(ambientCtx)
		capturedActor, _ = ctxkeys.ActorIDFrom(restored)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Saga journal carrier must overwrite the ambient "ambient-admin" with "system".
	if capturedActor != projection.SystemPrincipalActor {
		t.Errorf("RestoreContext actor = %q, want %q (SystemPrincipalActor); "+
			"saga journal carrier must always install system identity (ADR #1609 §5)",
			capturedActor, projection.SystemPrincipalActor)
	}
}

// ---------------------------------------------------------------------------
// Unit: Head / Position semantics
// ---------------------------------------------------------------------------

// TestSagaJournalSource_HeadMatchesGlobalSeq asserts Head() returns the
// GlobalSeq of the most recent journal event (=HeadSeq of the GlobalReader).
func TestSagaJournalSource_HeadMatchesGlobalSeq(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	head0, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head (empty): %v", err)
	}
	if head0 != 0 {
		t.Errorf("Head on empty journal = %d, want 0", head0)
	}

	const n = 3
	seedGlobalEvents(t, j, clk, n)

	head1, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head after %d events: %v", n, err)
	}
	if head1 < int64(n) {
		t.Errorf("Head = %d after seeding %d events, want >= %d", head1, n, n)
	}
}

// TestSagaJournalSource_PositionMatchesGlobalSeq asserts Position() returns the
// GlobalSeq of the event (1-based, monotonically increasing).
func TestSagaJournalSource_PositionMatchesGlobalSeq(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	const n = 4
	events := seedGlobalEvents(t, j, clk, n)
	if len(events) != n {
		t.Fatalf("seedGlobalEvents returned %d events, want %d", len(events), n)
	}

	for i, e := range events {
		pos, err := src.Position(e)
		if err != nil {
			t.Fatalf("Position(events[%d]): %v", i, err)
		}
		wantPos := int64(i + 1)
		if pos != wantPos {
			t.Errorf("Position(events[%d]) = %d, want %d (GlobalSeq)", i, pos, wantPos)
		}
	}
}

// TestSagaJournalSource_ReplayPagination asserts that Replay delivers ALL events
// in the journal even when internal pagination is required. This guards against a
// one-batch-only off-by-one in the pagination loop.
func TestSagaJournalSource_ReplayPagination(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	const n = 5
	want := seedGlobalEvents(t, j, clk, n)
	if len(want) != n {
		t.Fatalf("seedGlobalEvents returned %d events, want %d", len(want), n)
	}

	var got []projection.ProjectionEvent
	err := src.Replay(context.Background(), 0, func(e projection.ProjectionEvent) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// All seeded events must appear in got.
	wantIDs := make(map[string]struct{}, n)
	for _, e := range want {
		wantIDs[e.EventID()] = struct{}{}
	}
	matchCount := 0
	for _, e := range got {
		if _, ok := wantIDs[e.EventID()]; ok {
			matchCount++
		}
	}
	if matchCount != n {
		t.Errorf("Replay delivered %d/%d seeded events; total delivered = %d",
			matchCount, n, len(got))
	}
}

// ---------------------------------------------------------------------------
// seed helper
// ---------------------------------------------------------------------------

// seedGlobalEvents appends n saga journal events to j and returns them as
// []projection.ProjectionEvent via SagaJournalSource.Replay.
//
// Each call creates a FRESH instance so multiple calls on the SAME journal do not
// reuse the same instance ID (which would trigger ErrSagaDuplicateInstance).
func seedGlobalEvents(t *testing.T, j journal.Journal, clk *clockmock.FakeClock, n int) []projection.ProjectionEvent {
	t.Helper()
	// Use a unique instance ID per call to avoid duplicate-instance conflicts.
	instID := uniqueInstID()
	inst := sagajournaltest.NewInstanceFixture(t, instID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue(%s): %v", instID, err)
	}
	_, leaseID, err := j.ClaimPending(context.Background(), 100, time.Hour)
	if err != nil || leaseID == "" {
		t.Fatalf("ClaimPending: err=%v leaseID=%v", err, leaseID)
	}

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("journal %T does not implement journal.GlobalReader", j)
	}
	src := sagaprojection.NewSagaJournalSource(gr)

	beforeHead, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head before seed: %v", err)
	}

	for range n {
		if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
			Kind:     journal.KindStepStarted,
			StepName: "step-one", // KindStepStarted requires a non-empty StepName (ValidateForAppend)
		}); err != nil {
			t.Fatalf("Append(%s): %v", instID, err)
		}
	}

	var results []projection.ProjectionEvent
	if err := src.Replay(context.Background(), beforeHead, func(e projection.ProjectionEvent) error {
		results = append(results, e)
		return nil
	}); err != nil {
		t.Fatalf("seed Replay: %v", err)
	}
	if len(results) < n {
		t.Fatalf("seed produced %d events via Replay, want at least %d", len(results), n)
	}
	return results[:n]
}

// seedCounter is a monotonic counter for generating unique instance IDs across
// multiple seedGlobalEvents calls in the same test binary. It is intentionally
// non-atomic: seedGlobalEvents is always called from a single goroutine (the test
// runner sequential driver), and each parallel sub-test uses its own journal.
var seedCounter int

// uniqueInstID returns a unique instance ID string for each call.
func uniqueInstID() string {
	seedCounter++
	return "seed-inst-" + itoa(seedCounter)
}

// itoa converts an int to its decimal string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
