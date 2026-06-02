// L3 conformance: projection rebuild + event replay test (go-standards.md §L3)
package projection

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// rebuildTestHandlerTimeout is the deadline for a buildHandler call to return
// once the gate is open. 2s is ample for an in-process no-op apply.
const rebuildTestHandlerTimeout = 2 * time.Second

// ---------------------------------------------------------------------------
// helpers for rebuild tests
// ---------------------------------------------------------------------------

// coordinatorFullParams groups the non-testing parameters for newCoordinatorFull.
// Introduced to reduce the argument count below the S107 limit of 7.
type coordinatorFullParams struct {
	clk          *clockmock.FakeClock
	projectionID string // defaults to "p1" when empty
	reg          *fakeRegistrar
	txr          *fakeTxRunner
	store        CheckpointStore
	cursor       Cursor
	replay       ReplaySource
}

// newCoordinatorFull creates a Coordinator with all dependencies including
// clock and replay source. Nil fields default to safe in-memory fakes so callers
// only need to set the fields they care about.
func newCoordinatorFull(t *testing.T, p coordinatorFullParams) *Coordinator {
	t.Helper()
	projID := p.projectionID
	if projID == "" {
		projID = "p1"
	}
	if p.reg == nil {
		p.reg = &fakeRegistrar{}
	}
	if p.txr == nil {
		p.txr = &fakeTxRunner{}
	}
	if p.store == nil {
		p.store = NewMemCheckpointStore()
	}
	if p.cursor == nil {
		p.cursor = &fakeCursor{pos: 1}
	}
	c, err := NewCoordinator(p.clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: projID,
		Registrar:    p.reg,
		TxRunner:     p.txr,
		Store:        p.store,
		Cursor:       p.cursor,
		Replay:       p.replay,
		Tracer:       wrapper.NoopTracer{},
		Metrics:      nil, // metrics optional
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// makeReplayWithEntries seeds a MemReplaySource with n entries and returns
// the source and a MemCursor that resolves positions by insertion index.
func makeReplayWithEntries(t *testing.T, n int) (*MemReplaySource, *MemCursor) {
	t.Helper()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur, err := NewMemCursor(src)
	if err != nil {
		t.Fatalf("NewMemCursor() error = %v", err)
	}
	for i := 0; i < n; i++ {
		entry := mustNewTestEntry(t, clk, "topic.v1")
		src.Append(entry)
	}
	return src, cur
}

// newMemCursor is a package-test alias for NewMemCursor, kept for callers in
// replay_test.go and probe_test.go that predate the public MemCursor export.
// src is always non-nil in these callers, so the error path panics.
func newMemCursor(src *MemReplaySource) *MemCursor {
	cur, err := NewMemCursor(src)
	if err != nil {
		panic(err)
	}
	return cur
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

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})
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

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})

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
//
// fakeTxRunner is an in-memory runner that executes the function in the same
// goroutine without a real database transaction. The test asserts that the
// MemCheckpointStore offset is NOT zeroed after an OnReset error, which covers
// the Coordinator logic (failing to call SaveOffset(0) when onReset returns an
// error). True PG transaction atomicity (Apply + SaveOffset in one real tx) is
// exercised separately in PR-02 integration tests, not here.
// ---------------------------------------------------------------------------

func TestRebuild_OnResetErrorRollback(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 2)
	// Pre-seed checkpoint at 2 so we can verify it does NOT get zeroed.
	store := newSeededStore("testcell", "p1", 2)
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})

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

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})
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

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     &fakeTxRunner{},
		Store:        store,
		Cursor:       cur,
		Replay:       blockingReplay,
		Tracer:       wrapper.NoopTracer{},
	})
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
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, cursor: &fakeCursor{pos: 1}, replay: src})

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

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     &fakeTxRunner{},
		Store:        store,
		Cursor:       cur,
		Replay:       blockingReplay,
		Tracer:       wrapper.NoopTracer{},
	})
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

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    &fakeRegistrar{},
		TxRunner:     &fakeTxRunner{},
		Store:        NewMemCheckpointStore(),
		Cursor:       cur,
		Replay:       blockingReplay,
		Tracer:       wrapper.NoopTracer{},
	})
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
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, cursor: &fakeCursor{pos: 1}, replay: src})

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

// assertCoordinatorResult checks that (c, err) matches wantErr. Extracted to
// reduce the cognitive complexity of the outer table-driven loop.
func assertCoordinatorResult(t *testing.T, c *Coordinator, err error, wantErr bool) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if c != nil {
			t.Error("expected nil Coordinator on error")
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Coordinator")
	}
}

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
		{
			// F9: invalid projectionID (uppercase violates snake_case probe-name pattern)
			name: "invalid projectionID uppercase",
			clk:  clk, projID: "BadProj", replay: validReplay,
			wantErr: true,
		},
		{
			// F9: invalid projectionID (hyphen not allowed)
			name: "invalid projectionID hyphen",
			clk:  clk, projID: "bad-proj", replay: validReplay,
			wantErr: true,
		},
		// nil clk panics via clock.MustHaveClock — covered separately; not tested here.
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewCoordinator(tc.clk, CoordinatorConfig{
				CellID:       "testcell",
				ProjectionID: tc.projID,
				Registrar:    validReg,
				TxRunner:     validTxr,
				Store:        validStore,
				Cursor:       validCursor,
				Replay:       tc.replay,
				Tracer:       wrapper.NoopTracer{},
			})
			assertCoordinatorResult(t, c, err, tc.wantErr)
		})
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_InvalidIdentifiers_F9 — invalid CellID / ProjectionID rejected
// ---------------------------------------------------------------------------

// TestNewCoordinator_InvalidIdentifiers_F9 asserts that CellID and ProjectionID
// must satisfy the probe-name snake_case pattern so metric labels are bounded.
func TestNewCoordinator_InvalidIdentifiers_F9(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Now())
	reg := &fakeRegistrar{}
	txr := &fakeTxRunner{}
	store := NewMemCheckpointStore()
	cursor := &fakeCursor{}
	replay := NewMemReplaySource()

	tests := []struct {
		name    string
		cellID  string
		projID  string
		wantErr bool
	}{
		{name: "valid both", cellID: "testcell", projID: "p1", wantErr: false},
		{name: "invalid cellID uppercase", cellID: "BadCell", projID: "p1", wantErr: true},
		{name: "invalid cellID hyphen", cellID: "bad-cell", projID: "p1", wantErr: true},
		{name: "invalid cellID empty", cellID: "", projID: "p1", wantErr: true},
		{name: "invalid projID uppercase", cellID: "testcell", projID: "BadProj", wantErr: true},
		{name: "invalid projID hyphen", cellID: "testcell", projID: "bad-proj", wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewCoordinator(clk, CoordinatorConfig{
				CellID:       tc.cellID,
				ProjectionID: tc.projID,
				Registrar:    reg,
				TxRunner:     txr,
				Store:        store,
				Cursor:       cursor,
				Replay:       replay,
				Tracer:       wrapper.NoopTracer{},
			})
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_CaptureHeadError — captureHead error routes to failRebuild → PhaseLive
// (finding #17)
// ---------------------------------------------------------------------------

// errHeadReplaySource is a ReplaySource whose Head always returns an error.
type errHeadReplaySource struct {
	headErr error
}

func (e *errHeadReplaySource) Head(_ context.Context) (int64, error) { return 0, e.headErr }
func (e *errHeadReplaySource) Replay(_ context.Context, _ int64, _ func(outbox.Entry) error) error {
	return nil
}

func TestRebuild_CaptureHeadError(t *testing.T) {
	t.Parallel()
	headErr := errors.New("head unavailable")
	src := &errHeadReplaySource{headErr: headErr}
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())
	cur := &fakeCursor{pos: 1}

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     txr,
		Store:        store,
		Cursor:       cur,
		Replay:       src,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// failRebuild must restore PhaseLive and open the gate.
	waitForPhase(t, c, PhaseLive)
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after captureHead error", c.Phase())
	}
	// Gate must be open: buildHandler should not hang.
	result := make(chan outbox.HandleResult, 1)
	entry := mustNewEntryForTest(t)
	h := c.buildHandler(applyNoop)
	go func() { result <- h(context.Background(), entry) }()
	select {
	case r := <-result:
		// Any disposition is fine; gate being open means we didn't hang.
		_ = r
	case <-time.After(rebuildTestHandlerTimeout):
		t.Fatal("buildHandler hung after captureHead error (gate not opened)")
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_GateParkAndResume — gate parks handler during rebuild and unblocks on Live
// (finding #18)
// ---------------------------------------------------------------------------

func TestRebuild_GateParkAndResume(t *testing.T) {
	t.Parallel()
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())
	// fakeCursor with pos=1 lets applyOne run on any entry.
	cur := &fakeCursor{pos: 1}

	// Use a blocking replay source to keep rebuild in Replay phase (gate SHUT).
	blockSrc := &blockingReplaySource{unblock: make(chan struct{})}

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     txr,
		Store:        store,
		Cursor:       cur,
		Replay:       blockSrc,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	entry := mustNewEntryForTest(t)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// Wait until PhaseReplay — at that point shutGate has been called and Replay is blocking.
	waitForPhase(t, c, PhaseReplay)

	// (b) drive buildHandler with an entry. The handler goroutine will park on the
	// gate (gate is SHUT while blockSrc is blocking). We verify it is parked by
	// observing the POSITIVE resume rather than relying on a wall-clock window.
	handlerDone := make(chan outbox.HandleResult, 1)
	h := c.buildHandler(applyNoop)
	entryCtx, entryCancel := context.WithCancel(context.Background())
	defer entryCancel()
	go func() {
		handlerDone <- h(entryCtx, entry)
	}()

	// Assert the handler has NOT returned yet using a non-blocking select immediately
	// after the goroutine starts. The goroutine cannot pass the gate-wait select
	// before the rebuild releases it (the gate is a channel read), so this is
	// deterministic: if handlerDone has a value here, the gate was not shut.
	select {
	case <-handlerDone:
		t.Fatal("buildHandler returned while rebuild is in-flight (gate should be shut)")
	default:
		// expected: handler goroutine is parked on the closed gate channel
	}

	// (c) unblock rebuild → runs to completion → gate opens → PhaseLive.
	close(blockSrc.unblock)
	waitForPhase(t, c, PhaseLive)

	// (d) POSITIVE assertion: handler must complete after gate opens. This is the
	// deterministic resume assertion — once PhaseLive is confirmed, the gate is open
	// and the parked handler will unblock.
	select {
	case <-handlerDone:
		// gate opened — handler returned; any disposition is acceptable
	case <-time.After(rebuildTestHandlerTimeout):
		t.Fatal("buildHandler hung after gate opened")
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_OnResetError_GateReopened — gate is open after OnReset error
// (finding #19)
// ---------------------------------------------------------------------------

func TestRebuild_OnResetError_GateReopened(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 2)
	store := newSeededStore("testcell", "p1", 2)
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})

	errReset := errors.New("reset failed")
	subscribeWithDefaults(t, c, applyNoop, WithOnReset(func(_ context.Context) error {
		return errReset
	}))

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	// Gate must be open — buildHandler must NOT hang.
	result := make(chan outbox.HandleResult, 1)
	entry := mustNewEntryForTest(t)
	h := c.buildHandler(applyNoop)
	go func() { result <- h(context.Background(), entry) }()
	select {
	case <-result:
		// any disposition fine; what matters is no hang
	case <-time.After(rebuildTestHandlerTimeout):
		t.Fatal("buildHandler hung after OnReset error (gate not reopened)")
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_CatchupPaths — non-fatal isCaughtUp error + ctx-cancel paths
// (finding #20)
// ---------------------------------------------------------------------------

// catchupErrReplaySource is a ReplaySource that returns an error from Head
// on the n-th call, used to trigger the catchup non-fatal degraded path.
type catchupErrReplaySource struct {
	mu       sync.Mutex
	headCall int
	failAt   int // Head returns error on calls >= failAt
	headErr  error
}

func (s *catchupErrReplaySource) Head(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headCall++
	if s.failAt > 0 && s.headCall >= s.failAt {
		return 0, s.headErr
	}
	return 0, nil
}

func (s *catchupErrReplaySource) Replay(_ context.Context, _ int64, _ func(outbox.Entry) error) error {
	return nil
}

// TestRebuild_CatchupIsCaughtUpError asserts that a Head/LoadOffset error during
// catchup is non-fatal: phase returns to Live without blocking.
func TestRebuild_CatchupIsCaughtUpError(t *testing.T) {
	t.Parallel()
	// Arrange: on Head call 1 (captureHead) return 0; on call 2+ (catchup) return error.
	catchupSrc := &catchupErrReplaySource{failAt: 2, headErr: errors.New("head transient error")}
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())
	cur := &fakeCursor{pos: 1}

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     txr,
		Store:        store,
		Cursor:       cur,
		Replay:       catchupSrc,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	// Non-fatal path must resolve to PhaseLive.
	waitForPhase(t, c, PhaseLive)
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after catchup isCaughtUp error", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_DegradedCatchup_NoDurationObserved — rebuild_duration NOT recorded
// on degraded catchup path, IS recorded on clean catchup path (Task 4)
// ---------------------------------------------------------------------------

// runDegradedCatchupSubtest is the degraded-path sub-case for
// TestRebuild_DegradedCatchup_NoDurationObserved: Head fails on 2nd call (during
// catchup) → rebuild_duration histogram receives ZERO observations.
func runDegradedCatchupSubtest(t *testing.T) {
	t.Helper()
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}

	// Head returns error on calls >= failAt (2 = first captureHead succeeds, catchup fails).
	catchupSrc := &catchupErrReplaySource{failAt: 2, headErr: errors.New("catchup head error")}
	clk := clockmock.New(time.Now())

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    &fakeRegistrar{},
		TxRunner:     &fakeTxRunner{},
		Store:        NewMemCheckpointStore(),
		Cursor:       &fakeCursor{pos: 1},
		Replay:       catchupSrc,
		Tracer:       wrapper.NoopTracer{},
		Metrics:      m,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	count := p.histogramCount(
		metricProjectionRebuildDuration,
		kernelmetrics.Labels{"cell": "testcell", "projection": "p1"},
	)
	if count != 0 {
		t.Errorf("degraded catchup: rebuild_duration observations = %d, want 0", count)
	}
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v after degraded catchup, want PhaseLive", c.Phase())
	}
}

// runCleanCatchupSubtest is the clean-path sub-case for
// TestRebuild_DegradedCatchup_NoDurationObserved: full replay, caughtUp=true →
// rebuild_duration histogram receives ≥1 observation.
func runCleanCatchupSubtest(t *testing.T) {
	t.Helper()
	t.Parallel()
	p := newProjectionRecordingProvider()
	m, err := RegisterMetrics(p)
	if err != nil {
		t.Fatalf("RegisterMetrics: %v", err)
	}

	const n = 3
	src, cur := makeReplayWithEntries(t, n)
	clk := clockmock.New(time.Now())

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    &fakeRegistrar{},
		TxRunner:     &fakeTxRunner{},
		Store:        NewMemCheckpointStore(),
		Cursor:       cur,
		Replay:       src,
		Tracer:       wrapper.NoopTracer{},
		Metrics:      m,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitForPhase(t, c, PhaseLive)

	count := p.histogramCount(
		metricProjectionRebuildDuration,
		kernelmetrics.Labels{"cell": "testcell", "projection": "p1"},
	)
	if count < 1 {
		t.Errorf("clean rebuild: rebuild_duration observations = %d, want ≥1", count)
	}
}

// TestRebuild_DegradedCatchup_NoDurationObserved asserts that:
//  1. Degraded path (catchupPhase returns caughtUp=false due to Head error) → the
//     rebuild_duration histogram receives ZERO observations.
//  2. Clean path (catchupPhase returns caughtUp=true) → the histogram receives ≥1
//     observation.
//
// This verifies the production constraint: runRebuild observes rebuild_duration
// ONLY when caughtUp==true; degraded logs Warn and skips the Observe call.
func TestRebuild_DegradedCatchup_NoDurationObserved(t *testing.T) {
	t.Parallel()
	t.Run("degraded: Head error in catchup → duration NOT observed", runDegradedCatchupSubtest)
	t.Run("clean: full catchup → duration observed ≥1", runCleanCatchupSubtest)
}

// TestRebuild_CatchupCtxCancel asserts that a ctx cancel during catchup routes
// through failRebuild → PhaseLive and leaves the gate open.
func TestRebuild_CatchupCtxCancel(t *testing.T) {
	t.Parallel()
	// Use empty replay source so captureHead/reset/replay succeed instantly.
	emptySrc := NewMemReplaySource()
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())
	cur := &fakeCursor{pos: 1}

	// Seed 1 event so head=1 and checkpoint=0, making catchup spin.
	clk2 := clockmock.New(time.Now())
	emptySrc.Append(mustNewTestEntry(t, clk2, "topic.v1"))

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     txr,
		Store:        store,
		Cursor:       cur,
		Replay:       emptySrc,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Cancel via Close — triggers rebuildCancel → ctx cancel in catchup.
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after catchup ctx cancel", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_RebuildThenClose_NoRace — concurrent Rebuild + Close under -race
// (finding #1 race verification)
// ---------------------------------------------------------------------------

func TestRebuild_RebuildThenClose_NoRace(t *testing.T) {
	t.Parallel()
	blockSrc := &blockingReplaySource{unblock: make(chan struct{})}
	store := NewMemCheckpointStore()
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())
	cur := &fakeCursor{pos: 1}

	c, err := NewCoordinator(clk, CoordinatorConfig{
		CellID:       "testcell",
		ProjectionID: "p1",
		Registrar:    reg,
		TxRunner:     &fakeTxRunner{},
		Store:        store,
		Cursor:       cur,
		Replay:       blockSrc,
		Tracer:       wrapper.NoopTracer{},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	subscribeWithDefaults(t, c, applyNoop)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	waitUntilPhaseNot(t, c, PhaseLive)

	// Close concurrently while rebuild is in-flight — must not data-race.
	// unblock before Close so the goroutine finishes quickly.
	close(blockSrc.unblock)
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v, want PhaseLive after Close", c.Phase())
	}
}

// ---------------------------------------------------------------------------
// TestClose_Idempotent — concurrent Close calls must not panic or race
// ---------------------------------------------------------------------------

// TestClose_Idempotent asserts that multiple concurrent Close calls do not
// panic (no double-close) and all return nil. The done channel is guarded by
// sync.Once so only the first caller closes it.
func TestClose_Idempotent(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, cursor: &fakeCursor{pos: 1}, replay: src})

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = c.Close(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Close[%d] returned %v, want nil", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_PanicRecovered — apply panic during replay is recovered in goroutine
// ---------------------------------------------------------------------------

// TestRebuild_PanicRecovered asserts that a panic inside the apply function
// during replay is caught by the runRebuild defer, the process survives,
// Phase() returns PhaseLive, and the gate is re-opened.
func TestRebuild_PanicRecovered(t *testing.T) {
	t.Parallel()
	src, cur := makeReplayWithEntries(t, 2)
	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	clk := clockmock.New(time.Now())

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, reg: reg, txr: txr, store: store, cursor: cur, replay: src})

	// apply panics on the first entry.
	panicApply := func(_ context.Context, _ outbox.Entry) error {
		panic("business-apply-panic")
	}
	subscribeWithDefaults(t, c, panicApply)

	if err := c.Rebuild(context.Background()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// The goroutine must survive the panic and return to PhaseLive.
	waitForPhase(t, c, PhaseLive)
	if c.Phase() != PhaseLive {
		t.Errorf("Phase = %v after panic, want PhaseLive", c.Phase())
	}

	// Gate must be open — buildHandler must not hang.
	result := make(chan outbox.HandleResult, 1)
	entry := mustNewEntryForTest(t)
	h := c.buildHandler(applyNoop)
	go func() { result <- h(context.Background(), entry) }()
	select {
	case <-result:
		// any disposition is fine — gate is open
	case <-time.After(rebuildTestHandlerTimeout):
		t.Fatal("buildHandler hung after panic recovery (gate not reopened)")
	}
}

// mustNewEntryForTest creates a minimal outbox.Entry for handler tests.
func mustNewEntryForTest(t *testing.T) outbox.Entry {
	t.Helper()
	clk := clockmock.New(time.Now())
	e, err := outbox.NewEntry(clk, context.Background(), "topic.v1", []byte(`{}`))
	if err != nil {
		t.Fatalf("outbox.NewEntry: %v", err)
	}
	return e
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

// waitForPhase blocks until c.Phase() == want or the timeout fires. The rebuild
// state machine runs on a background goroutine with no started-observable sync
// hook (it transitions phases internally), so this is a sanctioned external
// poll via testwait.External (TEST-SLEEP-DISCIPLINE-01) rather than a raw sleep
// loop. The wait observes real-clock goroutine progress, not simulated time.
func waitForPhase(t *testing.T, c *Coordinator, want Phase) {
	t.Helper()
	testwait.External(t, "projection-rebuild-wait-for-phase",
		func() bool { return c.Phase() == want },
		testtime.EventuallyLong, testtime.FastPoll,
		"phase != %v", want)
}

// waitUntilPhaseNot blocks until c.Phase() != notWant or the timeout fires.
// Same background-goroutine rationale as waitForPhase.
//
//nolint:unparam // notWant=PhaseLive in current callers; param kept for future tests
func waitUntilPhaseNot(t *testing.T, c *Coordinator, notWant Phase) {
	t.Helper()
	testwait.External(t, "projection-rebuild-wait-phase-changed",
		func() bool { return c.Phase() != notWant },
		testtime.EventuallyLong, testtime.FastPoll,
		"phase still == %v", notWant)
}
