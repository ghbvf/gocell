// Package projectiontest provides conformance test helpers for
// projection.CheckpointStore, projection.ReplaySource, and projection.Cursor
// implementations.
//
// RunCheckpointConformance exercises the offset roundtrip contract that every
// CheckpointStore must satisfy:
//   - Cold start returns (0, nil)
//   - Save followed by Load returns the saved value
//   - Monotonically advancing saves are reflected immediately
//   - Keys are isolated (different (cellID, projectionID) pairs do not interfere)
//   - Re-saving the same offset is idempotent
//   - A backward write (lower offset) is accepted, not rejected (caller responsibility #1)
//
// RunReplaySourceConformance exercises the ReplaySource read contract (Head
// accounting, Replay delivery order, the from-offset skip, replay stability).
//
// RunCursorConformance exercises the four Cursor position invariants documented
// on projection.Cursor (1-based, monotonic, gap-allowed, permanent-error).
//
// The Checkpoint helper does NOT exercise transaction semantics (ambient-tx
// binding is the responsibility of each adapter's own integration test). The PG
// adapter (PR-02) invokes this funnel directly on a real
// *postgres.ProjectionCheckpointStore; bare-ctx calls route through the pool
// (each statement auto-commits), and tx atomicity (commit/rollback) is exercised
// separately in the adapter's own integration tests.
//
// stdlib-only: no external test frameworks are imported.
package projectiontest

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
)

const (
	testIsoCellA = "cellA-iso"
	testIsoProj1 = "p1-iso"

	testBackCell     = "cell-back"
	testBackProj     = "proj-back"
	fmtErrLoadOffset = "LoadOffset: %v"
)

// RunCheckpointConformance runs the six canonical conformance sub-tests against
// store. Each sub-test uses a distinct (cellID, projectionID) key to prevent
// ordering-dependent interference on a shared store instance.
//
// # Caller responsibilities
//
//  1. Monotonicity is the Coordinator's responsibility, not the store's. The
//     store must accept any int64 offset, including backward writes. MemCheckpointStore
//     overwrites unconditionally; a PG implementation must do the same (no
//     "reject-if-lower" guard) — deduplication is handled by the Coordinator's
//     pos <= checkpoint check.
//
//  2. Isolation before call: a durable store (e.g. PR-02 Postgres adapter) must
//     start from an empty or isolated namespace before calling this helper. Use
//     a separate schema, a per-test table prefix, or a TRUNCATE to prevent
//     interference from other tests or prior runs.
//
//  3. Transaction semantics are NOT asserted here: this helper only verifies
//     the offset roundtrip contract (SaveOffset → LoadOffset). Atomic commit
//     behavior (Apply + SaveOffset in a single transaction) is exercised in each
//     adapter's own integration test suite (PR-02 for Postgres).
//
// Usage (from a package that creates its own store):
//
//	func TestMyStore_Conformance(t *testing.T) {
//	    projectiontest.RunCheckpointConformance(t, mystore.New())
//	}
func RunCheckpointConformance(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	t.Run("ColdStartReturnsZero", func(t *testing.T) {
		t.Parallel()
		checkColdStart(t, store)
	})
	t.Run("SaveLoadRoundtrip", func(t *testing.T) {
		t.Parallel()
		checkSaveLoadRoundtrip(t, store)
	})
	t.Run("MonotonicAdvance", func(t *testing.T) {
		t.Parallel()
		checkMonotonicAdvance(t, store)
	})
	t.Run("KeyIsolation", func(t *testing.T) {
		t.Parallel()
		checkKeyIsolation(t, store)
	})
	t.Run("IdempotentReSave", func(t *testing.T) {
		t.Parallel()
		checkIdempotentReSave(t, store)
	})
	t.Run("BackwardWriteAccepted", func(t *testing.T) {
		t.Parallel()
		checkBackwardWrite(t, store)
	})
}

func checkColdStart(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	got, err := store.LoadOffset(ctx, "cell-cold", "proj-cold")
	if err != nil {
		t.Fatalf("LoadOffset: unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("cold-start LoadOffset = %d, want 0", got)
	}
}

func checkSaveLoadRoundtrip(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveOffset(ctx, "cell-rt", "proj-rt", 7); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}
	got, err := store.LoadOffset(ctx, "cell-rt", "proj-rt")
	if err != nil {
		t.Fatalf(fmtErrLoadOffset, err)
	}
	if got != 7 {
		t.Errorf("LoadOffset = %d, want 7", got)
	}
}

func checkMonotonicAdvance(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	for _, want := range []int64{1, 5, 9} {
		if err := store.SaveOffset(ctx, "cell-mono", "proj-mono", want); err != nil {
			t.Fatalf("SaveOffset(%d): %v", want, err)
		}
		got, err := store.LoadOffset(ctx, "cell-mono", "proj-mono")
		if err != nil {
			t.Fatalf("LoadOffset after save(%d): %v", want, err)
		}
		if got != want {
			t.Errorf("after Save(%d): LoadOffset = %d, want %d", want, got, want)
		}
	}
}

func checkKeyIsolation(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveOffset(ctx, testIsoCellA, testIsoProj1, 3); err != nil {
		t.Fatalf("SaveOffset cellA/p1: %v", err)
	}
	if err := store.SaveOffset(ctx, "cellB-iso", testIsoProj1, 4); err != nil {
		t.Fatalf("SaveOffset cellB/p1: %v", err)
	}
	if err := store.SaveOffset(ctx, testIsoCellA, "p2-iso", 5); err != nil {
		t.Fatalf("SaveOffset cellA/p2: %v", err)
	}
	checkIsolationEntry(t, store, ctx, testIsoCellA, testIsoProj1, 3)
	checkIsolationEntry(t, store, ctx, "cellB-iso", testIsoProj1, 4)
	checkIsolationEntry(t, store, ctx, testIsoCellA, "p2-iso", 5)
}

func checkIsolationEntry(t *testing.T, store projection.CheckpointStore, ctx context.Context, cellID, projID string, want int64) {
	t.Helper()
	got, err := store.LoadOffset(ctx, cellID, projID)
	if err != nil {
		t.Fatalf("LoadOffset(%s/%s): %v", cellID, projID, err)
	}
	if got != want {
		t.Errorf("LoadOffset(%s/%s) = %d, want %d", cellID, projID, got, want)
	}
}

// checkBackwardWrite asserts the store accepts a lower offset after a higher one
// (no "reject-if-lower" guard). Deduplication is the Coordinator's job (the
// pos <= checkpoint skip in applyOne), so the store must overwrite
// unconditionally — see caller-responsibility #1 below. This guards against a
// future implementation adding a monotonicity CHECK that the other monotonic-
// advance sub-test would not catch (it only writes ascending values).
func checkBackwardWrite(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveOffset(ctx, testBackCell, testBackProj, 9); err != nil {
		t.Fatalf("SaveOffset(9): %v", err)
	}
	if err := store.SaveOffset(ctx, testBackCell, testBackProj, 3); err != nil {
		t.Fatalf("SaveOffset(3) backward write must be accepted, not rejected: %v", err)
	}
	got, err := store.LoadOffset(ctx, testBackCell, testBackProj)
	if err != nil {
		t.Fatalf(fmtErrLoadOffset, err)
	}
	if got != 3 {
		t.Errorf("after backward Save(3): LoadOffset = %d, want 3 (unconditional overwrite)", got)
	}
}

func checkIdempotentReSave(t *testing.T, store projection.CheckpointStore) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := store.SaveOffset(ctx, "cell-idem", "proj-idem", 5); err != nil {
			t.Fatalf("SaveOffset(5) attempt %d: %v", i, err)
		}
	}
	got, err := store.LoadOffset(ctx, "cell-idem", "proj-idem")
	if err != nil {
		t.Fatalf(fmtErrLoadOffset, err)
	}
	if got != 5 {
		t.Errorf("LoadOffset after two Save(5) = %d, want 5", got)
	}
}

// RunReplaySourceConformance verifies the canonical ReplaySource contract for
// src: Head accounting, Replay delivery ordering, the from-offset skip, and
// replay stability across calls.
//
// seed persists n fresh entries into src's backing store (the caller wires the
// concrete persistence: a mem source appends; a PG source inserts via the real
// outbox writer) and returns them in append order. The suite never writes to
// src itself, so a production read-only ReplaySource needs NO test-only seed
// method on its public surface.
//
// Numeric stream positions are deliberately NOT asserted here — that is the
// Cursor's contract, verified by RunCursorConformance. Ordering is checked by
// entry identity (Entry.ID()), which is robust to a shared, concurrently-seeded
// backing store: other callers' entries may interleave, so this suite asserts
// only that its own seeded entries appear in append order (an ordered
// subsequence of what Replay delivers).
//
// This conformance helper is intended for external packages (e.g. adapters) that
// provide their own ReplaySource implementations. Calling it from within the
// projection package itself would create an import cycle.
func RunReplaySourceConformance(t *testing.T, src projection.ReplaySource, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	t.Run("HeadAccounting", func(t *testing.T) {
		checkReplayHeadAccounting(t, src, seed)
	})
	t.Run("ReplayDeliversSeededInOrder", func(t *testing.T) {
		checkReplayDeliversInOrder(t, src, seed)
	})
	t.Run("ReplayFromOffsetSkipsPrior", func(t *testing.T) {
		checkReplayFromOffsetSkips(t, src, seed)
	})
	t.Run("ReplayStableAcrossCalls", func(t *testing.T) {
		checkReplayStable(t, src, seed)
	})
}

// checkReplayHeadAccounting asserts Head advances by at least n after seeding n
// entries. ">=" (not "==") tolerates a shared backing store accumulating entries
// from earlier sub-tests or concurrent callers.
func checkReplayHeadAccounting(t *testing.T, src projection.ReplaySource, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	before, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	seed(3)
	after, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if after < before+3 {
		t.Errorf("Head = %d after seeding 3 (was %d), want >= %d", after, before, before+3)
	}
}

// checkReplayDeliversInOrder seeds entries after recording the current head and
// asserts Replay(head) delivers them as an ordered subsequence.
func checkReplayDeliversInOrder(t *testing.T, src projection.ReplaySource, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	before, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	want := idsOf(seed(4))
	got := collectReplayIDs(t, src, before)
	assertOrderedSubsequence(t, got, want)
	assertEachDeliveredOnce(t, got, want)
}

// checkReplayFromOffsetSkips seeds a first batch, captures the head, seeds a
// second batch, and asserts Replay(head) excludes the first batch and delivers
// the second in order. This is the resume-from-checkpoint contract.
func checkReplayFromOffsetSkips(t *testing.T, src projection.ReplaySource, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	firstIDs := toSet(idsOf(seed(2)))
	mid, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	second := idsOf(seed(2))
	got := collectReplayIDs(t, src, mid)
	for _, id := range got {
		if firstIDs[id] {
			t.Errorf("Replay(from=%d) delivered entry %s seeded before the offset; it must be skipped", mid, id)
		}
	}
	assertOrderedSubsequence(t, got, second)
}

// checkReplayStable asserts two Replay(before) calls deliver an identical ID
// sequence (deterministic, stable positions — not ephemeral).
func checkReplayStable(t *testing.T, src projection.ReplaySource, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	before, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	want := idsOf(seed(3))
	first := collectReplayIDs(t, src, before)
	again := collectReplayIDs(t, src, before)
	if !equalStrings(first, again) {
		t.Errorf("Replay not stable across calls:\n first = %v\n again = %v", first, again)
	}
	assertOrderedSubsequence(t, first, want)
}

// RunCursorConformance verifies the four Cursor position invariants documented
// on projection.Cursor (cursor.go):
//
//  1. 1-based: every resolved position is >= 1.
//  2. Monotonic: positions advance across distinct entries delivered in order.
//  3. Gap-allowed: positions need not be contiguous — only strictly increasing
//     across distinct entries (this sub-test does NOT require pos[i+1]==pos[i]+1).
//  4. Permanent-error: an entry the backing store does not know yields a
//     permanent error (outbox.PermanentError), never a transient one.
//
// seed persists n fresh entries into the backing store the cursor resolves
// against (same contract as RunReplaySourceConformance.seed) and returns them in
// stream order. newUnseeded constructs a fresh entry that is NOT persisted —
// used to exercise the permanent-error path. A read-only production Cursor needs
// no test-only method: the caller wires both closures.
func RunCursorConformance(
	t *testing.T,
	cursor projection.Cursor,
	seed func(n int) []projection.ProjectionEvent,
	newUnseeded func() projection.ProjectionEvent,
) {
	t.Helper()
	t.Run("OneBasedMonotonicGapAllowed", func(t *testing.T) {
		checkCursorOneBasedMonotonic(t, cursor, seed)
	})
	t.Run("PermanentErrorOnUnknownEntry", func(t *testing.T) {
		checkCursorPermanentOnUnknown(t, cursor, newUnseeded)
	})
}

// checkCursorOneBasedMonotonic seeds distinct entries in order and asserts their
// resolved positions are >= 1 and strictly increasing (gaps allowed).
func checkCursorOneBasedMonotonic(t *testing.T, cursor projection.Cursor, seed func(n int) []projection.ProjectionEvent) {
	t.Helper()
	entries := seed(4)
	var prev int64
	for i, e := range entries {
		pos, err := cursor.Position(e)
		if err != nil {
			t.Fatalf("Position(seeded[%d]): unexpected error: %v", i, err)
		}
		if pos < 1 {
			t.Errorf("Position(seeded[%d]) = %d, want >= 1 (Cursor 1-based invariant)", i, pos)
		}
		if i > 0 && pos <= prev {
			t.Errorf("positions not strictly increasing at %d: %d <= %d "+
				"(monotonic invariant; gaps allowed but distinct entries must advance)", i, pos, prev)
		}
		prev = pos
	}
}

// checkCursorPermanentOnUnknown asserts the cursor returns a permanent error for
// an entry the backing store has never seen (cursor.go invariant #4: an
// unresolvable entry is unrecoverable, not a transient retry).
func checkCursorPermanentOnUnknown(t *testing.T, cursor projection.Cursor, newUnseeded func() projection.ProjectionEvent) {
	t.Helper()
	unknown := newUnseeded()
	_, err := cursor.Position(unknown)
	if err == nil {
		t.Fatalf("Position(unseeded entry) = nil error, want a permanent error")
	}
	var permErr *outbox.PermanentError
	if !errors.As(err, &permErr) {
		t.Errorf("Position(unseeded entry) error %v is not an *outbox.PermanentError; "+
			"an unresolvable entry must be permanent (cursor.go invariant #4), not transient", err)
	}
}

// ─── shared ID-based ordering helpers ──────────────────────────────────────────

// idsOf returns the Entry.ID() of each entry, preserving order.
func idsOf(entries []projection.ProjectionEvent) []string {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.EventID()
	}
	return ids
}

// collectReplayIDs replays from fromOffset and returns the delivered entry IDs
// in delivery order.
func collectReplayIDs(t *testing.T, src projection.ReplaySource, fromOffset int64) []string {
	t.Helper()
	var ids []string
	if err := src.Replay(context.Background(), fromOffset, func(e projection.ProjectionEvent) error {
		ids = append(ids, e.EventID())
		return nil
	}); err != nil {
		t.Fatalf("Replay(from=%d): %v", fromOffset, err)
	}
	return ids
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertEachDeliveredOnce fails t unless every id in want appears EXACTLY once in
// got. This rejects duplicate delivery of a seeded event — the ordered-subsequence
// check alone would tolerate a source that re-delivers a seeded entry (a real
// exactly-once hazard), so the two assertions are complementary.
func assertEachDeliveredOnce(t *testing.T, got, want []string) {
	t.Helper()
	count := make(map[string]int, len(got))
	for _, id := range got {
		count[id]++
	}
	for _, id := range want {
		if count[id] != 1 {
			t.Errorf("seeded entry %s was delivered %d times, want exactly 1 (no duplicate replay)", id, count[id])
		}
	}
}

// assertOrderedSubsequence fails t unless every id in want appears in got in the
// same relative order (got may contain additional interleaved entries from a
// shared backing store).
func assertOrderedSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, id := range got {
		if i < len(want) && id == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Errorf("Replay did not deliver the seeded entries in order:\n want subsequence = %v\n got = %v\n matched %d/%d",
			want, got, i, len(want))
	}
}
