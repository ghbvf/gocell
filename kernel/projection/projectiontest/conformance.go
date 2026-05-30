// Package projectiontest provides conformance test helpers for
// projection.CheckpointStore implementations.
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
// The helper does NOT exercise transaction semantics (ambient-tx binding is the
// responsibility of each adapter's own integration test). The PG adapter (PR-02)
// invokes this funnel directly on a real *postgres.ProjectionCheckpointStore;
// bare-ctx calls route through the pool (each statement auto-commits), and tx
// atomicity (commit/rollback) is exercised separately in the adapter's own
// integration tests.
//
// stdlib-only: no external test frameworks are imported.
package projectiontest

import (
	"context"
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

// replayAppenderPositioner is the interface MemReplaySource must satisfy to
// participate in conformance testing. Append adds entries; Position returns the
// 1-based insertion index for any entry the source has seen.
type replayAppenderPositioner interface {
	Append(outbox.Entry)
	Position(outbox.Entry) int64
}

// RunReplaySourceConformance verifies the canonical conformance sub-tests
// for a ReplaySource implementation: Head accounting, Replay ordering, and
// the 1-based position invariant.
//
// src must also implement the unexported replayAppenderPositioner interface
// (Append + Position); if not, the test is skipped. MemReplaySource satisfies
// this automatically. seedEntries creates n fresh entries for each sub-test.
//
// This conformance helper is intended for external packages (e.g. adapters) that
// provide their own ReplaySource implementations. Calling it from within the
// projection package itself would create an import cycle.
func RunReplaySourceConformance(t *testing.T, src projection.ReplaySource, seedEntries func(n int) []outbox.Entry) {
	t.Helper()
	ap, ok := src.(replayAppenderPositioner)
	if !ok {
		t.Skip("RunReplaySourceConformance: src does not implement Append+Position")
		return
	}
	t.Run("HeadEmptyAfterInit", func(t *testing.T) {
		t.Parallel()
		checkReplayHeadEmpty(t, src)
	})
	t.Run("HeadEqualsNAfterAppend", func(t *testing.T) {
		t.Parallel()
		checkReplayHeadN(t, src, ap, seedEntries)
	})
	t.Run("ReplayFromZeroAscending", func(t *testing.T) {
		t.Parallel()
		checkReplayAscending(t, src, ap, seedEntries)
	})
	t.Run("Position1Based", func(t *testing.T) {
		t.Parallel()
		checkReplayPosition1Based(t, src, ap, seedEntries)
	})
}

func checkReplayHeadEmpty(t *testing.T, src projection.ReplaySource) {
	t.Helper()
	head, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 0 {
		t.Logf("Head = %d (src may not be empty — caller should pass a freshly created src)", head)
	}
}

func checkReplayHeadN(t *testing.T, src projection.ReplaySource, ap replayAppenderPositioner, seedEntries func(n int) []outbox.Entry) {
	t.Helper()
	entries := seedEntries(3)
	for _, e := range entries {
		ap.Append(e)
	}
	head, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head < 3 {
		t.Errorf("Head = %d, want >= 3 after appending 3 entries", head)
	}
}

func checkReplayAscending(t *testing.T, src projection.ReplaySource, ap replayAppenderPositioner, seedEntries func(n int) []outbox.Entry) {
	t.Helper()
	// Record head before this sub-test's appends so fromOffset is relative to
	// this sub-test's 4 entries only — callers must pass a fresh src (or accept
	// that earlier sub-tests' entries are also replayed, which satisfies the
	// ascending invariant too since positions are global-monotonic).
	headBefore, _ := src.Head(context.Background())
	entries := seedEntries(4)
	for _, e := range entries {
		ap.Append(e)
	}
	var received []int64
	_ = src.Replay(context.Background(), headBefore, func(e outbox.Entry) error {
		received = append(received, ap.Position(e))
		return nil
	})
	if len(received) < 4 {
		t.Fatalf("received %d entries, want >= 4", len(received))
	}
	for i := 1; i < len(received); i++ {
		if received[i] <= received[i-1] {
			t.Errorf("positions not ascending at index %d: %d <= %d", i, received[i], received[i-1])
		}
	}
}

func checkReplayPosition1Based(
	t *testing.T, src projection.ReplaySource, ap replayAppenderPositioner, seedEntries func(n int) []outbox.Entry,
) {
	t.Helper()
	entries := seedEntries(2)
	for _, e := range entries {
		ap.Append(e)
	}
	_ = src.Replay(context.Background(), 0, func(e outbox.Entry) error {
		pos := ap.Position(e)
		if pos < 1 {
			t.Errorf("Position = %d, want >= 1 (Cursor 1-based invariant)", pos)
		}
		return nil
	})
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
