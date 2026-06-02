package projection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// snapshotTestLagOffset is the fixed last-applied age for the "recent apply"
// snapshot case (lag ≈ this value). Extracted to a package-level const per
// TEST-TIME-LITERAL-01.
const snapshotTestLagOffset = 10 * time.Second

// TestCoordinator_Snapshot_Values exercises the value cases of Coordinator.Snapshot:
// the Phase is always reported, and PendingEvents / ReplayLagSeconds are derived
// from the replay head, checkpoint, and last-applied domain time via the shared
// computeLagPending helper (same source as the lag probe).
func TestCoordinator_Snapshot_Values(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		headEntries  int   // number of replay entries (= head)
		checkpoint   int64 // stored offset
		lastAppliedT bool  // when true, lastApplied = now-lagOffset; else 0 (startup grace)
		wantPending  int64
		wantLagSecs  float64
	}{
		{name: "live empty", headEntries: 0, checkpoint: 0, lastAppliedT: false, wantPending: 0, wantLagSecs: 0},
		{
			name: "pending with recent apply", headEntries: 2, checkpoint: 0,
			lastAppliedT: true, wantPending: 2, wantLagSecs: snapshotTestLagOffset.Seconds(),
		},
		{name: "pending startup grace", headEntries: 2, checkpoint: 0, lastAppliedT: false, wantPending: 2, wantLagSecs: 0},
		{name: "negative pending clamped", headEntries: 0, checkpoint: 5, lastAppliedT: true, wantPending: 0, wantLagSecs: 0},
		{name: "caught up", headEntries: 2, checkpoint: 2, lastAppliedT: true, wantPending: 0, wantLagSecs: 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			clk := clockmock.New(now)
			entryClk := clockmock.New(now)
			src := NewMemReplaySource()
			for i := 0; i < tc.headEntries; i++ {
				src.Append(mustNewTestEntry(t, entryClk, "topic.v1"))
			}
			store := NewMemCheckpointStore()
			if tc.checkpoint > 0 {
				if err := store.SaveOffset(context.Background(), "testcell", "p1", tc.checkpoint); err != nil {
					t.Fatalf("SaveOffset: %v", err)
				}
			}
			c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, store: store, replay: src})
			if tc.lastAppliedT {
				c.lastAppliedUnixNano.Store(now.Add(-snapshotTestLagOffset).UnixNano())
			}

			snap, err := c.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("Snapshot: unexpected error: %v", err)
			}
			if snap.Phase != PhaseLive {
				t.Errorf("Phase = %v, want PhaseLive", snap.Phase)
			}
			if snap.PendingEvents != tc.wantPending {
				t.Errorf("PendingEvents = %d, want %d", snap.PendingEvents, tc.wantPending)
			}
			if snap.ReplayLagSeconds != tc.wantLagSecs {
				t.Errorf("ReplayLagSeconds = %v, want %v", snap.ReplayLagSeconds, tc.wantLagSecs)
			}
		})
	}
}

// TestCoordinator_Snapshot_Degraded asserts that a checkpoint-store / replay read
// error yields a Snapshot carrying the valid Phase with zeroed pending/lag plus a
// non-nil error — the caller (the rebuild HTTP handler) logs it but does not fail
// an already-admitted rebuild over a degraded snapshot read.
func TestCoordinator_Snapshot_Degraded(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clk := clockmock.New(now)
	headErr := errors.New("replay head unavailable")
	c := newCoordinatorFull(t, coordinatorFullParams{
		clk:    clk,
		store:  NewMemCheckpointStore(),
		replay: &errHeadReplaySource{headErr: headErr},
	})

	snap, err := c.Snapshot(context.Background())
	if err == nil {
		t.Fatal("Snapshot: expected non-nil error on replay Head failure, got nil")
	}
	if !errors.Is(err, headErr) {
		t.Errorf("error does not wrap headErr: %v", err)
	}
	if snap.Phase != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive (always populated even on degraded read)", snap.Phase)
	}
	if snap.PendingEvents != 0 || snap.ReplayLagSeconds != 0 {
		t.Errorf("degraded snapshot must zero pending/lag, got pending=%d lag=%v", snap.PendingEvents, snap.ReplayLagSeconds)
	}
}

// TestCoordinator_Snapshot_DegradedLoadOffset covers the second computeLagPending
// error edge — replay.Head succeeds but the checkpoint store's LoadOffset fails.
// Snapshot must still return a valid Phase with zeroed pending/lag + the error.
func TestCoordinator_Snapshot_DegradedLoadOffset(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	src.Append(mustNewTestEntry(t, clockmock.New(time.Now()), "topic.v1")) // Head succeeds (=1)
	store := &seededStore{offsets: make(map[string]int64), loadErr: errors.New("checkpoint load failed")}
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, store: store, replay: src})

	snap, err := c.Snapshot(context.Background())
	if err == nil {
		t.Fatal("Snapshot: expected non-nil error on LoadOffset failure, got nil")
	}
	if !errors.Is(err, store.loadErr) {
		t.Errorf("error does not wrap loadErr: %v", err)
	}
	if snap.Phase != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive", snap.Phase)
	}
	if snap.PendingEvents != 0 || snap.ReplayLagSeconds != 0 {
		t.Errorf("degraded snapshot must zero pending/lag, got pending=%d lag=%v", snap.PendingEvents, snap.ReplayLagSeconds)
	}
}

// TestCoordinator_Snapshot_PhaseReflectsCoordinator asserts Snapshot.Phase mirrors
// Coordinator.Phase() (in-memory read, never errors).
func TestCoordinator_Snapshot_PhaseReflectsCoordinator(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, replay: NewMemReplaySource()})

	// Force a non-live phase to prove Snapshot reads the live atomic, not a constant.
	c.phase.Store(uint32(PhaseReplay))
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Phase != PhaseReplay {
		t.Errorf("Phase = %v, want PhaseReplay (must reflect c.Phase())", snap.Phase)
	}
}
