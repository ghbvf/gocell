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
//
// The helper does NOT exercise transaction semantics (ambient-tx binding is the
// responsibility of each adapter's own integration test). PG adapter (PR-02)
// passes a tx-wrapping store to this funnel and exercises commit/rollback
// separately.
//
// stdlib-only: no external test frameworks are imported.
package projectiontest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/projection"
)

// RunCheckpointConformance runs the five canonical conformance sub-tests against
// store. Each sub-test uses a distinct (cellID, projectionID) key to prevent
// ordering-dependent interference on a shared store instance.
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
		t.Fatalf("LoadOffset: %v", err)
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
	if err := store.SaveOffset(ctx, "cellA-iso", "p1-iso", 3); err != nil {
		t.Fatalf("SaveOffset cellA/p1: %v", err)
	}
	if err := store.SaveOffset(ctx, "cellB-iso", "p1-iso", 4); err != nil {
		t.Fatalf("SaveOffset cellB/p1: %v", err)
	}
	if err := store.SaveOffset(ctx, "cellA-iso", "p2-iso", 5); err != nil {
		t.Fatalf("SaveOffset cellA/p2: %v", err)
	}
	checkIsolationEntry(t, store, ctx, "cellA-iso", "p1-iso", 3)
	checkIsolationEntry(t, store, ctx, "cellB-iso", "p1-iso", 4)
	checkIsolationEntry(t, store, ctx, "cellA-iso", "p2-iso", 5)
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
		t.Fatalf("LoadOffset: %v", err)
	}
	if got != 5 {
		t.Errorf("LoadOffset after two Save(5) = %d, want 5", got)
	}
}
