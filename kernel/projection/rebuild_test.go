package projection

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// helpers for rebuild tests
// ---------------------------------------------------------------------------

// newCoordinatorFull creates a Coordinator with all dependencies including
// clock and replay source. projectionID defaults to "p1".
func newCoordinatorFull(
	t *testing.T,
	clk *clockmock.FakeClock,
	projectionID string,
	reg *fakeRegistrar,
	txr *fakeTxRunner,
	store CheckpointStore,
	cursor Cursor,
	replay ReplaySource,
) *Coordinator {
	t.Helper()
	if projectionID == "" {
		projectionID = "p1"
	}
	c, err := NewCoordinator(
		clk,
		"testcell", projectionID,
		reg, txr, store, cursor, replay,
		wrapper.NoopTracer{},
		nil, // metrics optional
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// makeReplayWithEntries seeds a MemReplaySource with n entries and returns
// the source and a MemCursor that resolves positions by insertion index.
func makeReplayWithEntries(t *testing.T, n int) (*MemReplaySource, *memCursor) {
	t.Helper()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	for i := 0; i < n; i++ {
		entry := mustNewTestEntry(t, clk, "topic.v1")
		src.Append(entry)
	}
	return src, cur
}

// memCursor is a Cursor that uses MemReplaySource.positionOf to resolve the
// position of replayed entries. This ensures MemReplaySource position scheme
// and the Cursor used during replay agree (Cursor/Replay position coupling
// requirement from spec). Position returns the 1-based insertion index of the
// entry as stored by MemReplaySource.
type memCursor struct {
	src *MemReplaySource
}

func newMemCursor(src *MemReplaySource) *memCursor {
	return &memCursor{src: src}
}

func (c *memCursor) Position(e outbox.Entry) (int64, error) {
	pos := c.src.positionOf(e)
	if pos < 1 {
		return 0, errors.New("memCursor: entry not found in replay source")
	}
	return pos, nil
}

// subscribeWithDefaults calls Subscribe on c with a minimal spec and apply fn.
func subscribeWithDefaults(t *testing.T, c *Coordinator, apply Apply, opts ...Option) {
	t.Helper()
	spec := minimalSpec("projection.p1.v1")
	if err := c.Subscribe(context.Background(), spec, apply, opts...); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
}

// applyNoop is a no-op apply function.
func applyNoop(_ context.Context, _ outbox.Entry) error { return nil }

// ---------------------------------------------------------------------------
// TestRebuild_ColdFull — cold rebuild replays all events and ends PhaseLive
// ---------------------------------------------------------------------------

func TestRebuild_ColdFull(t *testing.T) {
	t.Parallel()
	const n = 5
	src, cur := makeReplayWithEntries(t, n)
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, clk, "p1", reg, txr, store, cur, src)
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Wait for rebuild to complete.
	waitForPhase(t, c, PhaseLive)

	// All events replayed — checkpoint should be at head=n.
	head, _ := src.Head(context.Background())
	cp, _ := store.LoadOffset(context.Background(), "testcell", "p1")
	if cp != head {
		t.Errorf("checkpoint = %d, want %d (head)", cp, head)
	}
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_OnResetCalledInTx
// ---------------------------------------------------------------------------

func TestRebuild_OnResetCalledInTx(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 3)
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, clk, "p1", reg, txr, store, cur, src)

	var resetCalls int32
	onReset := func(ctx context.Context) error {
		atomic.AddInt32(&resetCalls, 1)
		return nil
	}
	subscribeWithDefaults(t, c, applyNoop, WithOnReset(onReset))

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	if atomic.LoadInt32(&resetCalls) != 1 {
		t.Errorf("onReset calls = %d, want 1", atomic.LoadInt32(&resetCalls))
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_OnResetErrorRollback — OnReset error must not zero the offset
// ---------------------------------------------------------------------------

func TestRebuild_OnResetErrorRollback(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 2)
	// Pre-seed checkpoint at 2 so we can verify it does NOT get zeroed.
	store := newSeededStore("testcell", "p1", 2)
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, clk, "p1", reg, txr, store, cur, src)

	errReset := errors.New("reset failed")
	onReset := func(ctx context.Context) error {
		return errReset
	}
	subscribeWithDefaults(t, c, applyNoop, WithOnReset(onReset))

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	// Offset must NOT have been zeroed — OnReset error causes whole tx rollback.
	cp, _ := store.LoadOffset(context.Background(), "testcell", "p1")
	if cp != 2 {
		t.Errorf("checkpoint = %d, want 2 (OnReset error must not zero offset)", cp)
	}
	// Phase must return to Live (not stuck in Reset).
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after OnReset error", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_ReplayErrorReturnLive — mid-replay error ends in PhaseLive
// ---------------------------------------------------------------------------

func TestRebuild_ReplayErrorReturnLive(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 5)
	store := NewMemCheckpointStore()

	// applyErr after entry 2 to simulate mid-replay error.
	var applyCalls int32
	errApply := errors.New("apply error")
	apply := func(ctx context.Context, e outbox.Entry) error {
		n := atomic.AddInt32(&applyCalls, 1)
		if n == 3 {
			return errApply
		}
		return nil
	}

	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, clk, "p1", reg, txr, store, cur, src)
	subscribeWithDefaults(t, c, apply)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	// Phase must return to Live even after replay error.
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after replay error", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_ConcurrentSecondRebuild — ErrRebuildInProgress
// ---------------------------------------------------------------------------

func TestRebuild_ConcurrentSecondRebuild(t *testing.T) {
	t.Parallel()
	store := NewMemCheckpointStore()
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	// Empty source so rebuild hangs at Replay with no events (immediately completes
	// Stopped/Reset phase). We just need to test the CAS behavior.
	// Set phase to Stopped manually via CAS to simulate in-flight rebuild.
	// Simplest approach: build a source that never returns from Replay.
	blockingReplay := &blockingReplaySource{unblock: make(chan struct{})}
	cur := &fakeCursor{pos: 1}

	c, err := NewCoordinator(
		clk,
		"testcell", "p1",
		reg, &fakeTxRunner{}, store, cur, blockingReplay,
		wrapper.NoopTracer{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	// First Rebuild — should succeed (CAS Live→Stopped).
	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("first Rebuild: %v", err)
	}

	// Wait until phase changes from Live (rebuild is in-flight).
	waitUntilPhaseNot(t, c, PhaseLive)

	// Second Rebuild while first is still in-flight — must return ErrRebuildInProgress.
	err = c.Rebuild(context.Background())
	if !errors.Is(err, ErrRebuildInProgress) {
		t.Errorf("second Rebuild error = %v, want ErrRebuildInProgress", err)
	}

	// Unblock and wait for rebuild to complete.
	close(blockingReplay.unblock)
	waitForPhase(t, c, PhaseLive)
}

// ---------------------------------------------------------------------------
// TestRebuild_BeforeSubscribe — error before Subscribe
// ---------------------------------------------------------------------------

func TestRebuild_BeforeSubscribe(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, clk, "p1", &fakeRegistrar{}, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{pos: 1}, src)

	// Rebuild before Subscribe must return an error.
	err := c.Rebuild(context.Background())
	if err == nil {
		t.Fatal("Rebuild before Subscribe: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestClose_CancelsInFlightRebuild
// ---------------------------------------------------------------------------

func TestClose_CancelsInFlightRebuild(t *testing.T) {
	t.Parallel()
	store := NewMemCheckpointStore()
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	blockingReplay := &blockingReplaySource{unblock: make(chan struct{})}
	cur := &fakeCursor{pos: 1}

	c, err := NewCoordinator(
		clk,
		"testcell", "p1",
		reg, &fakeTxRunner{}, store, cur, blockingReplay,
		wrapper.NoopTracer{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitUntilPhaseNot(t, c, PhaseLive)

	// Close must cancel the rebuild and wait for goroutine completion.
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After Close, phase returns to Live.
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v after Close, want PhaseLive", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_ContextCancelReturnLive — context cancel → PhaseLive
// ---------------------------------------------------------------------------

func TestRebuild_ContextCancelReturnLive(t *testing.T) {
	t.Parallel()
	blockingReplay := &blockingReplaySource{unblock: make(chan struct{})}
	cur := &fakeCursor{pos: 1}
	clk := clockmock.New(time.Now())

	c, err := NewCoordinator(
		clk,
		"testcell", "p1",
		&fakeRegistrar{}, &fakeTxRunner{}, NewMemCheckpointStore(), cur, blockingReplay,
		wrapper.NoopTracer{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitUntilPhaseNot(t, c, PhaseLive)

	// Cancel via Close (which cancels rebuildCancel).
	close(blockingReplay.unblock) // unblock so Close doesn't hang
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after cancel", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestSubscribe_SecondCallError — v1 single input stream (Hard constraint)
// ---------------------------------------------------------------------------

func TestSubscribe_SecondCallError(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, clk, "p1", &fakeRegistrar{}, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{pos: 1}, src)

	spec := minimalSpec("projection.p1.v1")
	// First Subscribe succeeds.
	if err := c.Subscribe(context.Background(), spec, applyNoop); err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	// Second Subscribe must return error (already subscribed).
	err := c.Subscribe(context.Background(), spec, applyNoop)
	if err == nil {
		t.Fatal("second Subscribe: expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Errorf("error is not *errcode.Error: %T %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_NilGuards_PR03 — new params: clk, projectionID, replay
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilGuards_PR03(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Now())
	validReg := &fakeRegistrar{}
	validTxr := &fakeTxRunner{}
	validStore := NewMemCheckpointStore()
	validCursor := &fakeCursor{}
	validReplay := NewMemReplaySource()

	tests := []struct {
		name    string
		clk     *clockmock.FakeClock
		projID  string
		replay  ReplaySource
		wantErr bool
	}{
		{
			name: "all valid",
			clk:  clk, projID: "p1", replay: validReplay,
			wantErr: false,
		},
		{
			name: "empty projectionID",
			clk:  clk, projID: "", replay: validReplay,
			wantErr: true,
		},
		{
			name: "nil replay",
			clk:  clk, projID: "p1", replay: nil,
			wantErr: true,
		},
		// nil clk panics via clock.MustHaveClock — covered separately; not tested here.
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewCoordinator(
				tc.clk,
				"testcell", tc.projID,
				validReg, validTxr, validStore, validCursor, tc.replay,
				wrapper.NoopTracer{},
				nil,
			)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if c != nil {
					t.Error("expected nil Coordinator on error")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c == nil {
					t.Fatal("expected non-nil Coordinator")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// blockingReplaySource — ReplaySource that blocks until unblock is closed
// ---------------------------------------------------------------------------

type blockingReplaySource struct {
	unblock chan struct{}
}

func (b *blockingReplaySource) Replay(ctx context.Context, _ int64, _ func(outbox.Entry) error) error {
	select {
	case <-b.unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingReplaySource) Head(context.Context) (int64, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// waitForPhase / waitUntilPhaseNot — test sync helpers
// ---------------------------------------------------------------------------

// waitForPhase spins until c.Phase() == want or test times out.
//
//nolint:unparam // want=PhaseLive in current callers; param kept for future tests
func waitForPhase(t *testing.T, c *Coordinator, want Phase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Phase() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for phase %v; current = %v", want, c.Phase())
}

// waitUntilPhaseNot spins until c.Phase() != notWant or test times out.
func waitUntilPhaseNot(t *testing.T, c *Coordinator, notWant Phase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Phase() != notWant {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for phase != %v; current = %v", notWant, c.Phase())
}
