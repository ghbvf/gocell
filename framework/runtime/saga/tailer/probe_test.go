package tailer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/projection"
)

func newProbeTailer(t *testing.T, src *fakeSource, store projection.OwnerCheckpointStore) *Tailer {
	t.Helper()
	clk := clockmock.New(time.Unix(0, 0))
	return newTestTailer(t, src, store,
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, newTestLocker(t, clk), clk)
}

func TestTailer_ProbesName(t *testing.T) {
	tl := newProbeTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore())
	probes := tl.Probes()
	if len(probes) != 1 {
		t.Fatalf("Probes len = %d, want 1", len(probes))
	}
	if got := string(probes[0].Name()); got != "auditcore_saga_tailer_sagastatus_ready" {
		t.Errorf("probe name = %q, want auditcore_saga_tailer_sagastatus_ready", got)
	}
}

func TestTailer_CheckReadyLifecycleStates(t *testing.T) {
	tl := newProbeTailer(t, &fakeSource{events: events(1)}, projection.NewMemOwnerCheckpointStore())
	ctx := context.Background()

	// Default state is stopped → not ready.
	for _, st := range []tailerState{tailerStopped, tailerStarting, tailerStopping} {
		tl.state.Store(int32(st))
		if err := tl.checkReady(ctx); err == nil {
			t.Errorf("state %d: checkReady = nil, want not-ready", st)
		}
	}

	// Running + storage reachable → ready.
	tl.state.Store(int32(tailerRunning))
	if err := tl.checkReady(ctx); err != nil {
		t.Errorf("running+reachable: checkReady = %v, want ready", err)
	}
}

func TestTailer_CheckReadyStorageUnreachable(t *testing.T) {
	src := &fakeSource{headErr: errors.New("journal down")}
	tl := newProbeTailer(t, src, projection.NewMemOwnerCheckpointStore())
	tl.state.Store(int32(tailerRunning))
	if err := tl.checkReady(context.Background()); err == nil {
		t.Error("running but Head errors: checkReady = nil, want not-ready")
	}
}

func TestTailer_ComputePending(t *testing.T) {
	store := projection.NewMemOwnerCheckpointStore()
	if err := store.AdvanceIfOwner(context.Background(), testCell, testProj, "seed", 2); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tl := newProbeTailer(t, &fakeSource{events: events(1, 2, 3, 4, 5)}, store)
	pending, err := tl.computePending(context.Background())
	if err != nil {
		t.Fatalf("computePending: %v", err)
	}
	if pending != 3 { // head 5 − checkpoint 2
		t.Errorf("pending = %d, want 3", pending)
	}

	// Checkpoint ahead of head → clamp to 0.
	if err := store.AdvanceIfOwner(context.Background(), testCell, testProj, "seed", 99); err != nil {
		t.Fatalf("seed ahead: %v", err)
	}
	pending, err = tl.computePending(context.Background())
	if err != nil {
		t.Fatalf("computePending(ahead): %v", err)
	}
	if pending != 0 {
		t.Errorf("pending(ahead) = %d, want 0 (clamped)", pending)
	}
}
