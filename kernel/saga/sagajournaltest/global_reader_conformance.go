package sagajournaltest

import (
	"bytes"
	"context"
	"math"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
)

const loadSinceErrFmt = "LoadSince: %v"

// RunGlobalReaderConformance runs the conformance suite for the
// [journal.GlobalReader] global-ordered-scan contract (#1609 PR-02) against the
// supplied factory. It is deliberately SEPARATE from RunConformanceSuite (which
// covers the per-instance [journal.Journal] contract): GlobalReader is a narrow
// read-only interface a backend may implement independently, and a dedicated
// suite gives archtest SAGA-GLOBALREADER-CONFORMANCE-ENROLL-01 a precise anchor
// to require every GlobalReader implementation to enroll.
//
// It reuses [Factory] (returning journal.Journal) on purpose: generating a global
// sequence to scan requires driving appends through the full write surface
// (Enqueue/ClaimPending/Append/MarkTerminal), and in this design every
// GlobalReader implementation IS the saga_events store and therefore a full
// Journal (see the GlobalReader godoc — the narrow type is a consumer view, not a
// read-only-backend contract). The factory's Journal MUST also implement
// journal.GlobalReader; one that does not is a programmer error and fails the
// suite immediately (the enrollment archtest only routes GlobalReader
// implementations here).
//
// A dedicated GlobalReaderFactory returning a separate (driver Journal, reader
// GlobalReader) pair was considered and rejected: with driver==reader for every
// real store its only payoff would be a standalone read-only-backend conformance,
// which is unreachable anyway (a read-only store still needs some Journal writer
// to seed the very events it scans). It would add a speculative abstraction for a
// backend that does not exist — so the conformance binds to Journal, the honest
// shape of every GlobalReader implementation.
func RunGlobalReaderConformance(t *testing.T, factory Factory) {
	t.Helper()

	cases := []struct {
		name string
		run  func(*testing.T, Factory)
	}{
		// Global ordering across instances + the GlobalSeq/Event.GlobalSeq mirror.
		{"GlobalSeq_MonotonicAcrossInstances", conformGlobalSeqMonotonicAcrossInstances},
		// HeadSeq tracks the highest assigned position (0 when empty).
		{"HeadSeq_ReflectsAppends", conformGlobalHeadSeqReflectsAppends},
		// Cursor pagination: consecutive LoadSince pages are gap-free, overlap-free.
		{"LoadSince_Pagination", conformGlobalLoadSincePagination},
		// Cursor at/after head, and empty store, return empty (no error).
		{"LoadSince_PastHead_Empty", conformGlobalLoadSincePastHeadEmpty},
		// A pathologically large limit is clamped to the head (no int64 overflow / panic).
		{"LoadSince_HugeLimit_NoOverflow", conformGlobalLoadSinceHugeLimit},
		// Fail-closed argument validation.
		{"LoadSince_InvalidArgs_KindInvalid", conformGlobalLoadSinceInvalidArgs},
		// Terminal events (written via MarkTerminal) are visible to the global scan
		// — succeeded AND the compensation terminals, the raison d'être of model-A.
		{"TerminalEvent_SucceededInGlobalLog", conformGlobalTerminalEventScannable},
		{"TerminalEvent_CompensatedInGlobalLog", conformGlobalCompensatedTerminalScannable},
		// Returned payloads are defensive copies.
		{"GlobalEvent_DefensiveCopy", conformGlobalEventDefensiveCopy},
		// Concurrent cross-instance appends: global seq stays distinct + contiguous.
		{"GlobalSeq_ConcurrentAppend_NoDupContiguous", conformGlobalSeqConcurrentAppend},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, factory) })
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// newGlobalReaderFixture builds a fresh Journal from factory and asserts it also
// implements GlobalReader, returning both views plus the shared FakeClock. It
// calls cleanup itself before failing (the caller's deferred cleanup is not yet
// registered when this helper runs).
func newGlobalReaderFixture(t *testing.T, factory Factory) (journal.Journal, journal.GlobalReader, *clockmock.FakeClock, func()) {
	t.Helper()
	j, clk, cleanup := factory(t)
	gr, ok := j.(journal.GlobalReader)
	if !ok {
		cleanup()
		t.Fatalf("journal %T does not implement journal.GlobalReader", j)
	}
	return j, gr, clk, cleanup
}

// globalSeqs extracts the GlobalSeq of each event for diagnostic messages.
func globalSeqs(events []journal.GlobalEvent) []int64 {
	out := make([]int64, len(events))
	for i, ge := range events {
		out[i] = ge.GlobalSeq
	}
	return out
}

// ---------------------------------------------------------------------------
// Conformance cases
// ---------------------------------------------------------------------------

// conformGlobalSeqMonotonicAcrossInstances interleaves appends across two
// instances and asserts the global scan returns them in a single strictly
// ascending, gap-free GlobalSeq order with correct InstanceID attribution. It
// also locks the GlobalSeq == Event.GlobalSeq mirror invariant per event.
func conformGlobalSeqMonotonicAcrossInstances(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	instA := NewInstanceFixture(t, "inst-global-a", clk.Now())
	instB := NewInstanceFixture(t, "inst-global-b", clk.Now())
	mustEnqueue(t, j, instA)
	mustEnqueue(t, j, instB)
	_, leaseID := mustClaimAll(t, j) // one batch lease covers both instances

	// Interleave appends across the two instances (seq 1..4).
	appendStep(t, j, instA.ID, leaseID, journal.KindStepStarted)
	appendStep(t, j, instB.ID, leaseID, journal.KindStepStarted)
	appendStep(t, j, instA.ID, leaseID, journal.KindStepCompleted)
	appendStep(t, j, instB.ID, leaseID, journal.KindStepCompleted)

	events, err := gr.LoadSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf(loadSinceErrFmt, err)
	}
	if len(events) != 4 {
		t.Fatalf("LoadSince returned %d events; want 4 (seqs=%v)", len(events), globalSeqs(events))
	}

	// The suite drives appends serially (single goroutine), so for any conforming
	// backend the global order equals the append order — including PG, where the
	// IDENTITY column is assigned in commit order and these commits are serial on
	// one connection. Concurrent cross-instance ordering is covered separately by
	// GlobalSeq_ConcurrentAppend_NoDupContiguous, which asserts only
	// distinctness + contiguity rather than a specific interleaving.
	wantInstances := []idutil.SafeID{instA.ID, instB.ID, instA.ID, instB.ID}
	for i, ge := range events {
		wantSeq := int64(i + 1)
		if ge.GlobalSeq != wantSeq {
			t.Errorf("events[%d].GlobalSeq=%d; want %d", i, ge.GlobalSeq, wantSeq)
		}
		// Mirror invariant: the envelope GlobalSeq equals the embedded
		// Event.GlobalSeq (the value a projection cursor checkpoints on).
		if ge.Event.GlobalSeq != ge.GlobalSeq {
			t.Errorf("events[%d]: GlobalEvent.GlobalSeq=%d != Event.GlobalSeq=%d",
				i, ge.GlobalSeq, ge.Event.GlobalSeq)
		}
		if ge.InstanceID != wantInstances[i] {
			t.Errorf("events[%d].InstanceID=%q; want %q", i, ge.InstanceID, wantInstances[i])
		}
	}
	// Strictly ascending and contiguous (no gaps).
	for i := 1; i < len(events); i++ {
		if events[i].GlobalSeq != events[i-1].GlobalSeq+1 {
			t.Errorf("global seq not contiguous at index %d: %d after %d",
				i, events[i].GlobalSeq, events[i-1].GlobalSeq)
		}
	}
}

// conformGlobalHeadSeqReflectsAppends asserts HeadSeq is 0 on an empty journal
// and equals the number of appended events afterward.
func conformGlobalHeadSeqReflectsAppends(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	head, err := gr.HeadSeq(context.Background())
	if err != nil {
		t.Fatalf("HeadSeq (empty): %v", err)
	}
	if head != 0 {
		t.Errorf("HeadSeq on empty journal = %d; want 0", head)
	}

	inst := NewInstanceFixture(t, "inst-head-seq", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)

	// Derive the expected head from the actual append calls — no hardcoded count
	// that could silently drift from the statements below.
	kinds := []journal.EventKind{
		journal.KindStepStarted,   // Pending → Running
		journal.KindStepCompleted, // Running no-op
		journal.KindStepStarted,   // Running no-op
	}
	for _, k := range kinds {
		appendStep(t, j, inst.ID, leaseID, k)
	}
	wantHead := int64(len(kinds))

	head, err = gr.HeadSeq(context.Background())
	if err != nil {
		t.Fatalf("HeadSeq: %v", err)
	}
	if head != wantHead {
		t.Errorf("HeadSeq after %d appends = %d; want %d", wantHead, head, wantHead)
	}
}

// conformGlobalLoadSincePagination asserts consecutive LoadSince pages, advanced
// by the last GlobalSeq of the prior page, are contiguous and non-overlapping.
func conformGlobalLoadSincePagination(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-pagination", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)

	// 4 events: one StepStarted then three Running-phase no-op step events.
	appendStep(t, j, inst.ID, leaseID, journal.KindStepStarted)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepCompleted)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepCompleted)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepCompleted)

	page1, err := gr.LoadSince(context.Background(), 0, 2)
	if err != nil {
		t.Fatalf("LoadSince page1: %v", err)
	}
	if len(page1) != 2 || page1[0].GlobalSeq != 1 || page1[1].GlobalSeq != 2 {
		t.Fatalf("page1 seqs=%v; want [1 2]", globalSeqs(page1))
	}

	page2, err := gr.LoadSince(context.Background(), page1[len(page1)-1].GlobalSeq, 2)
	if err != nil {
		t.Fatalf("LoadSince page2: %v", err)
	}
	if len(page2) != 2 || page2[0].GlobalSeq != 3 || page2[1].GlobalSeq != 4 {
		t.Fatalf("page2 seqs=%v; want [3 4]", globalSeqs(page2))
	}

	// No overlap between pages and contiguous across the boundary.
	if page2[0].GlobalSeq != page1[len(page1)-1].GlobalSeq+1 {
		t.Errorf("pages not contiguous: page1 ends %d, page2 starts %d",
			page1[len(page1)-1].GlobalSeq, page2[0].GlobalSeq)
	}
}

// conformGlobalLoadSincePastHeadEmpty asserts an empty store, a cursor exactly
// at head, and a cursor past head all return an empty slice with a nil error.
func conformGlobalLoadSincePastHeadEmpty(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	empty, err := gr.LoadSince(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("LoadSince empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("LoadSince on empty store returned %d events; want 0", len(empty))
	}

	inst := NewInstanceFixture(t, "inst-past-head", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepStarted) // seq 1

	head, err := gr.HeadSeq(context.Background())
	if err != nil {
		t.Fatalf("HeadSeq: %v", err)
	}

	atHead, err := gr.LoadSince(context.Background(), head, 10)
	if err != nil {
		t.Fatalf("LoadSince at head: %v", err)
	}
	if len(atHead) != 0 {
		t.Errorf("LoadSince at head returned %d events; want 0", len(atHead))
	}

	pastHead, err := gr.LoadSince(context.Background(), head+100, 10)
	if err != nil {
		t.Fatalf("LoadSince past head: %v", err)
	}
	if len(pastHead) != 0 {
		t.Errorf("LoadSince past head returned %d events; want 0", len(pastHead))
	}
}

// conformGlobalLoadSinceHugeLimit asserts a pathologically large limit (one that
// would overflow int64 if naively added to the cursor) is clamped to the head
// and returns the remaining events without erroring or panicking. Regression for
// the start+limit int64 overflow → makeslice panic (PR-02 review F-overflow).
func conformGlobalLoadSinceHugeLimit(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-huge-limit", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepStarted)   // seq 1
	appendStep(t, j, inst.ID, leaseID, journal.KindStepCompleted) // seq 2

	// afterGlobalSeq > 0 with limit = MaxInt64: a naive start+limit overflows
	// int64 to a negative end → slice/makeslice panic. The implementation must
	// clamp to the head and return the single remaining event (seq 2).
	got, err := gr.LoadSince(context.Background(), 1, math.MaxInt64)
	if err != nil {
		t.Fatalf("LoadSince(after=1, limit=MaxInt64): %v", err)
	}
	if len(got) != 1 || got[0].GlobalSeq != 2 {
		t.Fatalf("LoadSince huge limit = %v; want exactly [seq 2]", globalSeqs(got))
	}

	// From the beginning with a huge limit returns everything, still no overflow.
	all, err := gr.LoadSince(context.Background(), 0, math.MaxInt64)
	if err != nil {
		t.Fatalf("LoadSince(after=0, limit=MaxInt64): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("LoadSince(0, MaxInt64) = %v; want 2 events", globalSeqs(all))
	}
}

// conformGlobalCompensatedTerminalScannable asserts the compensation terminal
// (Compensating → Compensated) is assigned a GlobalSeq and visible to the global
// scan. This is the core model-A guarantee: compensated/failed sagas emit NO bus
// event (compensation is emit-pure), so the journal global scan is the only way a
// projection can observe them.
func conformGlobalCompensatedTerminalScannable(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-compensated-global", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepStarted) // seq 1, Pending → Running
	if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
		Kind: journal.KindCompensationStarted, // seq 2, Running → Compensating
	}); err != nil {
		t.Fatalf("Append(CompensationStarted): %v", err)
	}
	appendStep(t, j, inst.ID, leaseID, journal.KindStepCompensated) // seq 3, legal while Compensating

	ok, err := j.MarkTerminal(context.Background(), inst.ID, leaseID, saga.StatusCompensated)
	if err != nil || !ok {
		t.Fatalf("MarkTerminal(Compensated): ok=%v err=%v", ok, err)
	}

	events, err := gr.LoadSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf(loadSinceErrFmt, err)
	}
	if len(events) != 4 {
		t.Fatalf("LoadSince returned %d events; want 4 (step+compStarted+compensated+terminal), seqs=%v",
			len(events), globalSeqs(events))
	}
	last := events[len(events)-1]
	if last.Event.Kind != journal.KindSagaCompensated {
		t.Errorf("terminal global event Kind=%v; want KindSagaCompensated", last.Event.Kind)
	}
	if last.GlobalSeq != 4 || last.InstanceID != inst.ID {
		t.Errorf("terminal event GlobalSeq=%d InstanceID=%q; want 4 / %q", last.GlobalSeq, last.InstanceID, inst.ID)
	}
}

// conformGlobalLoadSinceInvalidArgs asserts fail-closed validation: a
// non-positive limit or a negative cursor returns a KindInvalid error.
func conformGlobalLoadSinceInvalidArgs(t *testing.T, factory Factory) {
	t.Helper()
	_, gr, _, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	for _, limit := range []int{0, -1} {
		_, err := gr.LoadSince(context.Background(), 0, limit)
		if err == nil {
			t.Fatalf("LoadSince(limit=%d) should return error, got nil", limit)
		}
		if !isKindInvalid(err) {
			t.Errorf("LoadSince(limit=%d): want KindInvalid error, got %v", limit, err)
		}
	}

	_, err := gr.LoadSince(context.Background(), -1, 10)
	if err == nil {
		t.Fatal("LoadSince(afterGlobalSeq=-1) should return error, got nil")
	}
	if !isKindInvalid(err) {
		t.Errorf("LoadSince(afterGlobalSeq=-1): want KindInvalid error, got %v", err)
	}
}

// conformGlobalTerminalEventScannable asserts terminal events — written only via
// MarkTerminal — are assigned a GlobalSeq and are visible to the global scan
// (the entire reason for model-A: terminal states never reach the outbox).
func conformGlobalTerminalEventScannable(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-terminal-global", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)
	appendStep(t, j, inst.ID, leaseID, journal.KindStepStarted) // seq 1, Pending → Running

	ok, err := j.MarkTerminal(context.Background(), inst.ID, leaseID, saga.StatusSucceeded)
	if err != nil || !ok {
		t.Fatalf("MarkTerminal(Succeeded): ok=%v err=%v", ok, err)
	}

	events, err := gr.LoadSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf(loadSinceErrFmt, err)
	}
	if len(events) != 2 {
		t.Fatalf("LoadSince returned %d events; want 2 (step + terminal), seqs=%v",
			len(events), globalSeqs(events))
	}

	last := events[len(events)-1]
	if !last.Event.Kind.IsTerminal() {
		t.Errorf("last global event Kind=%v; want a terminal kind", last.Event.Kind)
	}
	if last.Event.Kind != journal.KindSagaSucceeded {
		t.Errorf("terminal global event Kind=%v; want KindSagaSucceeded", last.Event.Kind)
	}
	if last.GlobalSeq != 2 {
		t.Errorf("terminal event GlobalSeq=%d; want 2", last.GlobalSeq)
	}
	if last.InstanceID != inst.ID {
		t.Errorf("terminal event InstanceID=%q; want %q", last.InstanceID, inst.ID)
	}
}

// conformGlobalEventDefensiveCopy asserts mutating a payload returned by
// LoadSince does not corrupt the journal's internal copy.
func conformGlobalEventDefensiveCopy(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	inst := NewInstanceFixture(t, "inst-global-defensive", clk.Now())
	mustEnqueue(t, j, inst)
	_, leaseID := mustClaimAll(t, j)

	wantPayload := []byte(`{"k":"v"}`)
	if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: stepOne,
		Payload:  wantPayload,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	first, err := gr.LoadSince(context.Background(), 0, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("LoadSince: err=%v len=%d", err, len(first))
	}
	first[0].Event.Payload[0] = 'X' // mutate the returned copy

	second, err := gr.LoadSince(context.Background(), 0, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("LoadSince (second): err=%v len=%d", err, len(second))
	}
	if second[0].Event.Payload[0] == 'X' {
		t.Error("LoadSince returned a reference to internal payload; want a defensive copy")
	}
	if !bytes.Equal(second[0].Event.Payload, wantPayload) {
		t.Errorf("stored payload corrupted: got %q; want %q", second[0].Event.Payload, wantPayload)
	}
}

// conformGlobalSeqConcurrentAppend appends concurrently across multiple
// instances and asserts the global sequence is distinct and contiguous (1..N)
// with no duplicates — exercising the global counter under -race.
func conformGlobalSeqConcurrentAppend(t *testing.T, factory Factory) {
	t.Helper()
	j, gr, clk, cleanup := newGlobalReaderFixture(t, factory)
	defer cleanup()

	const instances = 3
	const perInstance = 8
	ids := make([]idutil.SafeID, instances)
	for i := range instances {
		inst := NewInstanceFixture(t, "inst-global-conc-"+string(rune('a'+i)), clk.Now())
		mustEnqueue(t, j, inst)
		ids[i] = inst.ID
	}
	_, leaseID := mustClaimAll(t, j)

	var wg sync.WaitGroup
	for _, id := range ids {
		for range perInstance {
			wg.Add(1)
			go func(id idutil.SafeID) {
				defer wg.Done()
				if _, err := j.Append(context.Background(), id, leaseID, journal.Event{
					Kind:     journal.KindStepStarted,
					StepName: stepOne,
				}); err != nil {
					t.Errorf("concurrent Append(%s): %v", id, err)
				}
			}(id)
		}
	}
	wg.Wait()

	total := instances * perInstance
	events, err := gr.LoadSince(context.Background(), 0, total*2)
	if err != nil {
		t.Fatalf(loadSinceErrFmt, err)
	}
	if len(events) != total {
		t.Fatalf("LoadSince returned %d events; want %d", len(events), total)
	}

	seen := make(map[int64]bool, total)
	for i, ge := range events {
		if ge.GlobalSeq != int64(i+1) {
			t.Errorf("events[%d].GlobalSeq=%d; want %d (not contiguous)", i, ge.GlobalSeq, i+1)
		}
		if seen[ge.GlobalSeq] {
			t.Errorf("duplicate GlobalSeq %d in concurrent append results", ge.GlobalSeq)
		}
		seen[ge.GlobalSeq] = true
	}
}
