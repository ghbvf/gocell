package projection

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// This file is the PR-00 "implementability proof" for the projection lifecycle
// harness ADR (docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md).
//
// PR-00 ships interface declarations only — the Coordinator + behavioral
// guarantees (exactly-once / crash recovery) land in PR-01..PR-06. To prove the
// ADR's frozen contracts are not vapor, this test:
//
//   - implements CheckpointStore with a fake (proves the interface is
//     implementable against the ambient-tx signature),
//   - binds a function to Apply (proves the hook shape is usable),
//   - wraps the fake through WrapCheckpointStoreForCell and asserts the sealed
//     marker behaves (non-nil + typed-nil rejection + Noop pass-through),
//   - exercises Phase.String()/Valid() across the full enum.

// fakeCheckpointStore proves CheckpointStore is implementable with the
// ambient-tx signature (ctx-only; a real adapter pulls the tx via
// persistence.TxFromContext inside Save/Load — same shape as outbox.Writer).
type fakeCheckpointStore struct {
	offsets map[string]int64
	noop    bool
}

func newFakeCheckpointStore() *fakeCheckpointStore {
	return &fakeCheckpointStore{offsets: map[string]int64{}}
}

func key(cellID, projectionID string) string { return cellID + "/" + projectionID }

func (f *fakeCheckpointStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	return f.offsets[key(cellID, projectionID)], nil
}

func (f *fakeCheckpointStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	f.offsets[key(cellID, projectionID)] = offset
	return nil
}

func (f *fakeCheckpointStore) Noop() bool { return f.noop }

// Compile-time proof the fake satisfies the interface.
var _ CheckpointStore = (*fakeCheckpointStore)(nil)

// sampleApply proves the Apply function type is usable for a real hook shape.
func sampleApply(_ context.Context, _ outbox.Entry) error { return nil }

var _ Apply = sampleApply

// Option must be expressible as a functional option closure.
var _ Option = func(*subscribeOptions) {}

func TestCheckpointStoreImplementable(t *testing.T) {
	t.Parallel()
	var s CheckpointStore = newFakeCheckpointStore()
	ctx := context.Background()
	// Cold start: an unknown (cellID, projectionID) pair must load offset 0 with
	// no error — the base case the harness relies on (ADR §6 threat row 1).
	if got, err := s.LoadOffset(ctx, "ordercell", "never-seen"); err != nil || got != 0 {
		t.Fatalf("cold-start LoadOffset = (%d, %v), want (0, nil)", got, err)
	}
	if err := s.SaveOffset(ctx, "ordercell", "ordersummary", 7); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}
	got, err := s.LoadOffset(ctx, "ordercell", "ordersummary")
	if err != nil {
		t.Fatalf("LoadOffset: %v", err)
	}
	if got != 7 {
		t.Errorf("LoadOffset = %d, want 7", got)
	}
}

func TestWrapCheckpointStoreForCell_Seals(t *testing.T) {
	t.Parallel()
	inner := newFakeCheckpointStore()
	wrapped := WrapCheckpointStoreForCell(inner)
	if wrapped == nil {
		t.Fatal("WrapCheckpointStoreForCell returned nil for a real store")
	}
	// The sealed marker still satisfies the base interface transparently.
	var _ CheckpointStore = wrapped
	if err := wrapped.SaveOffset(context.Background(), "c", "p", 1); err != nil {
		t.Fatalf("wrapped SaveOffset: %v", err)
	}
}

func TestWrapCheckpointStoreForCell_NilInputs(t *testing.T) {
	t.Parallel()
	// bare-nil interface
	if got := WrapCheckpointStoreForCell(nil); got != nil {
		t.Errorf("WrapCheckpointStoreForCell(nil) = %v, want nil", got)
	}
	// typed-nil interface (e.g. var s *fakeCheckpointStore) must also map to nil
	var typedNil *fakeCheckpointStore
	if got := WrapCheckpointStoreForCell(typedNil); got != nil {
		t.Errorf("WrapCheckpointStoreForCell(typed-nil) = %v, want nil "+
			"(typed-nil detection keeps Init() fail-fast guards working)", got)
	}
}

func TestWrapCheckpointStoreForCell_NoopPassThrough(t *testing.T) {
	t.Parallel()
	// Inner reports noop=true → wrapper must forward it (durable-mode rejection
	// depends on this; mirror of kernel/outbox cell_marker_test).
	nooper := &fakeCheckpointStore{offsets: map[string]int64{}, noop: true}
	wrapped := WrapCheckpointStoreForCell(nooper)
	n, ok := wrapped.(interface{ Noop() bool })
	if !ok {
		t.Fatal("sealed CellCheckpointStore does not expose Noop() bool")
	}
	if !n.Noop() {
		t.Error("Noop() = false, want true (pass-through from inner)")
	}
}

func TestWrapCheckpointStoreForCell_NonNooperReturnsFalse(t *testing.T) {
	t.Parallel()
	// A store that does not implement Nooper → wrapper reports false.
	wrapped := WrapCheckpointStoreForCell(nonNooperStore{})
	n, ok := wrapped.(interface{ Noop() bool })
	if !ok {
		t.Fatal("sealed CellCheckpointStore does not expose Noop() bool")
	}
	if n.Noop() {
		t.Error("Noop() = true, want false (inner is not a Nooper)")
	}
}

// nonNooperStore implements CheckpointStore but NOT Nooper.
type nonNooperStore struct{}

func (nonNooperStore) LoadOffset(context.Context, string, string) (int64, error) { return 0, nil }
func (nonNooperStore) SaveOffset(context.Context, string, string, int64) error   { return nil }

func TestPhaseStringAndValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		p     Phase
		str   string
		valid bool
	}{
		{PhaseLive, "live", true},
		{PhaseStopped, "stopped", true},
		{PhaseReset, "reset", true},
		{PhaseReplay, "replay", true},
		{PhaseCatchup, "catchup", true},
		{Phase(0), "invalid", false},
		{Phase(99), "phase(99)", false},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.str {
			t.Errorf("Phase(%d).String() = %q, want %q", c.p, got, c.str)
		}
		if got := c.p.Valid(); got != c.valid {
			t.Errorf("Phase(%d).Valid() = %v, want %v", c.p, got, c.valid)
		}
	}
}

func TestPhaseZeroValueIsInvalid(t *testing.T) {
	t.Parallel()
	// iota+1 ensures the zero value is never a valid phase, so an
	// uninitialised Phase cannot masquerade as PhaseLive.
	var zero Phase
	if zero.Valid() {
		t.Error("zero-value Phase reported Valid() = true; iota+1 must make 0 invalid")
	}
}
