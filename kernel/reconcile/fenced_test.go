package reconcile

import (
	"context"
	"errors"
	"testing"
)

// fakeFencedRepo is a minimal in-package FencedRepository for unit-testing the
// FencedWriter delegation + CAS classification (the public reconciletest fake is
// in another package; this one stays internal so the test can call the unexported
// newFencedWriter / withFencedWriter).
type fakeFencedRepo struct {
	highest map[string]uint64
	calls   []uint64
	err     error
}

func newFakeFencedRepo() *fakeFencedRepo { return &fakeFencedRepo{highest: map[string]uint64{}} }

func (r *fakeFencedRepo) ApplyFenced(_ context.Context, entityID string, epoch uint64, _ any) (bool, error) {
	r.calls = append(r.calls, epoch)
	if r.err != nil {
		return false, r.err
	}
	if epoch < r.highest[entityID] {
		return false, nil
	}
	r.highest[entityID] = epoch
	return true, nil
}

func TestFencedWriter_WriteDelegatesWithBoundEpoch(t *testing.T) {
	t.Parallel()
	repo := newFakeFencedRepo()
	w := newFencedWriter(repo, 7)

	if got := w.Epoch(); got != 7 {
		t.Fatalf("Epoch() = %d, want 7", got)
	}
	if err := w.Write(context.Background(), "dev-1", "cmd"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(repo.calls) != 1 || repo.calls[0] != 7 {
		t.Fatalf("ApplyFenced epochs = %v, want [7] (the bound lease epoch)", repo.calls)
	}
}

func TestFencedWriter_StaleEpochRejectedAsErrFencedWriteStale(t *testing.T) {
	t.Parallel()
	repo := newFakeFencedRepo()
	// Establish highest-seen = 5 via an epoch-5 writer.
	if err := newFencedWriter(repo, 5).Write(context.Background(), "dev-1", "fresh"); err != nil {
		t.Fatalf("epoch-5 write: %v", err)
	}
	// A stale epoch-3 writer (zombie leader) must be rejected.
	err := newFencedWriter(repo, 3).Write(context.Background(), "dev-1", "stale")
	if !errors.Is(err, ErrFencedWriteStale) {
		t.Fatalf("stale write err = %v, want ErrFencedWriteStale", err)
	}
}

func TestFencedWriter_RepoErrorWrapped(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("backend down")
	repo := newFakeFencedRepo()
	repo.err = sentinel
	err := newFencedWriter(repo, 1).Write(context.Background(), "dev-1", "x")
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapped backend error", err)
	}
	if errors.Is(err, ErrFencedWriteStale) {
		t.Fatalf("a backend I/O error must NOT be reported as stale")
	}
}

func TestFencedWriter_UnboundIsProgrammerError(t *testing.T) {
	t.Parallel()
	var w FencedWriter // zero value: no repo
	err := w.Write(context.Background(), "dev-1", "x")
	if !errors.Is(err, ErrFencedWriterUnbound) {
		t.Fatalf("unbound Write err = %v, want ErrFencedWriterUnbound", err)
	}
}

// TestFencedWriter_EqualEpochAccepted verifies that writing at the same epoch twice
// is accepted (equal epoch is not rejected by the monotonic CAS: epoch >= highest).
func TestFencedWriter_EqualEpochAccepted(t *testing.T) {
	t.Parallel()
	repo := newFakeFencedRepo()
	// First write at epoch 5 → accepted, highest becomes 5.
	if err := newFencedWriter(repo, 5).Write(context.Background(), "dev-1", "cmd-1"); err != nil {
		t.Fatalf("first write at epoch 5: %v", err)
	}
	// Second write at epoch 5 → still accepted (equal epoch ≥ highest).
	if err := newFencedWriter(repo, 5).Write(context.Background(), "dev-1", "cmd-2"); err != nil {
		t.Fatalf("second write at equal epoch 5: %v", err)
	}
	if len(repo.calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(repo.calls))
	}
}

// TestFencedWriter_HigherEpochAccepted verifies that a strictly higher epoch is
// accepted after a lower one has set the highest-seen mark.
func TestFencedWriter_HigherEpochAccepted(t *testing.T) {
	t.Parallel()
	repo := newFakeFencedRepo()
	// Write at epoch 5.
	if err := newFencedWriter(repo, 5).Write(context.Background(), "dev-1", "cmd-a"); err != nil {
		t.Fatalf("write at epoch 5: %v", err)
	}
	// Write at epoch 6 (higher) → accepted, highest advances to 6.
	if err := newFencedWriter(repo, 6).Write(context.Background(), "dev-1", "cmd-b"); err != nil {
		t.Fatalf("write at epoch 6: %v", err)
	}
	if repo.highest["dev-1"] != 6 {
		t.Fatalf("highest epoch = %d, want 6", repo.highest["dev-1"])
	}
}

func TestFencedWriterFrom_PresentAndAbsent(t *testing.T) {
	t.Parallel()
	// Absent: a ctx with no writer reports ok=false (single-process / no-fencing).
	if _, ok := FencedWriterFrom(context.Background()); ok {
		t.Fatal("FencedWriterFrom on a bare ctx must report ok=false")
	}
	// Present: the Loop's withFencedWriter seeds an epoch-bound writer.
	repo := newFakeFencedRepo()
	ctx := withFencedWriter(context.Background(), newFencedWriter(repo, 42))
	w, ok := FencedWriterFrom(ctx)
	if !ok {
		t.Fatal("FencedWriterFrom must report ok=true after withFencedWriter")
	}
	if w.Epoch() != 42 {
		t.Fatalf("recovered writer Epoch() = %d, want 42", w.Epoch())
	}
}
