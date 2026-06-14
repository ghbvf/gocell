// Package sagaprojection provides the bridge from kernel/saga/journal.GlobalReader
// to the projection harness (ReplaySource + Cursor + cellvocab.ProjectionEvent).
//
// PR-03 of EPIC #1609 (#1627).
package sagaprojection_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/kernel/projection/projectiontest"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/framework/kernel/saga/sagaprojection"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	// Append one journal event so we have something to Replay.
	events := seedGlobalEvents(t, j, clk, 1)
	if len(events) != 1 {
		t.Fatalf("seed returned %d events, want 1", len(events))
	}

	// Replay from offset 0 and capture the ctx actor after RestoreContext.
	var capturedActor string
	ambientCtx := ctxkeys.WithActorID(context.Background(), "ambient-admin")
	err = src.Replay(ambientCtx, 0, func(e projection.ProjectionEvent) error {
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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

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
// in the journal across a REAL internal page boundary. The seed count is
// batchSize+1 so the pagination loop must fetch a second page WITH content (one
// event lands on page 2); a first-page-only regression would silently drop the
// page-2 event and fail the completeness assertion. It also asserts strictly
// ascending GlobalSeq order to catch a boundary off-by-one that drops or
// duplicates the seam event.
func TestSagaJournalSource_ReplayPagination(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("factory journal %T does not implement journal.GlobalReader", j)
	}
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	// batchSize+1 forces the pagination loop to cross one full batch boundary with
	// content on the second page. Tracking the real constant (BatchSizeForTest)
	// keeps this test correct if batchSize ever changes.
	const n = sagaprojection.BatchSizeForTest + 1
	want := seedGlobalEvents(t, j, clk, n)
	if len(want) != n {
		t.Fatalf("seedGlobalEvents returned %d events, want %d", len(want), n)
	}

	var got []projection.ProjectionEvent
	err = src.Replay(context.Background(), 0, func(e projection.ProjectionEvent) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Completeness: every one of the batchSize+1 seeded events must be delivered.
	// With n > batchSize this only holds if Replay paged past the first batch.
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
		t.Errorf("Replay delivered %d/%d seeded events (batchSize=%d): a first-page-only "+
			"regression drops events past the batch boundary; total delivered = %d",
			matchCount, n, sagaprojection.BatchSizeForTest, len(got))
	}

	// Ordering + no-duplicate across the page seam: positions must be strictly
	// ascending (GlobalSeq monotonic), so an event is never delivered twice and
	// the page-2 events follow the page-1 events in order.
	var prev int64
	for i, e := range got {
		pos, perr := src.Position(e)
		if perr != nil {
			t.Fatalf("Position(got[%d]): %v", i, perr)
		}
		if pos <= prev {
			t.Errorf("got[%d] position %d not strictly greater than previous %d; pagination "+
				"must preserve ascending GlobalSeq order without duplicates at the seam", i, pos, prev)
		}
		prev = pos
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
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

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
// multiple seedGlobalEvents calls in the same test binary. Declared as atomic.Int64
// to avoid a data race when parallel sub-tests call seedGlobalEvents concurrently
// (each sub-test uses its own journal, but all share the counter).
var seedCounter atomic.Int64

// uniqueInstID returns a unique instance ID string for each call.
func uniqueInstID() string {
	n := seedCounter.Add(1)
	return "seed-inst-" + strconv.Itoa(int(n))
}

// ---------------------------------------------------------------------------
// TestSagaJournalSource_ReplayIncludesTerminalEvents
// ---------------------------------------------------------------------------

// TestSagaJournalSource_ReplayIncludesTerminalEvents verifies that MarkTerminal
// emits a terminal event (KindSagaSucceeded) that is visible to SagaJournalSource
// Replay. This covers the core semantic of the saga projection source: a projection
// of saga completion state must be able to observe final outcomes, not just step
// events.
//
// Relation to the conformance suite: the conformance suite (seedGlobalEvents) only
// seeds KindStepStarted events; this test seeds a full saga lifecycle ending with
// MarkTerminal so that the GlobalReader's terminal-kind ordering guarantee is
// exercised via Replay.
func TestSagaJournalSource_ReplayIncludesTerminalEvents(t *testing.T) {
	t.Parallel()

	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("journal %T does not implement journal.GlobalReader", j)
	}
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	// Enqueue and claim a fresh instance.
	instID := uniqueInstID()
	inst := sagajournaltest.NewInstanceFixture(t, instID, clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_, leaseID, err := j.ClaimPending(context.Background(), 100, time.Hour)
	if err != nil || leaseID == "" {
		t.Fatalf("ClaimPending: err=%v leaseID=%v", err, leaseID)
	}

	// Append one step event so the instance is in a Running state (required
	// before MarkTerminal can transition to StatusSucceeded).
	if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "step-one",
	}); err != nil {
		t.Fatalf("Append step: %v", err)
	}

	// Record the head position before MarkTerminal so we can Replay only the
	// new terminal event.
	beforeTerminal, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head before MarkTerminal: %v", err)
	}

	// Commit the terminal state. This appends KindSagaSucceeded to the journal.
	ok, err = j.MarkTerminal(context.Background(), inst.ID, leaseID, sagaprojection.StatusSucceededForTest)
	if err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	if !ok {
		t.Fatal("MarkTerminal returned ok=false; lease may have been stale")
	}

	// Replay from beforeTerminal and collect all delivered events.
	var got []projection.ProjectionEvent
	if err := src.Replay(context.Background(), beforeTerminal, func(e projection.ProjectionEvent) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// At least one event must have been delivered (the terminal event).
	if len(got) == 0 {
		t.Fatal("Replay delivered 0 events after MarkTerminal; terminal event must be visible via GlobalReader")
	}

	// The last delivered event's EventID must be distinct and the position must
	// be > beforeTerminal (it has a higher GlobalSeq).
	lastPos, err := src.Position(got[len(got)-1])
	if err != nil {
		t.Fatalf("Position(last): %v", err)
	}
	if lastPos <= beforeTerminal {
		t.Errorf("Position(last)=%d want > beforeTerminal=%d; terminal event must have a higher GlobalSeq",
			lastPos, beforeTerminal)
	}
}
