package projection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeTxRunner is a pass-through TxRunner that records how many times it was
// invoked and propagates the after-commit registry as the real runner does.
type fakeTxRunner struct {
	runs int
}

func (f *fakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	f.runs++
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark)
		return err
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

// fakeCursor returns a controlled (pos, err) pair.
type fakeCursor struct {
	pos int64
	err error
}

func (f *fakeCursor) Position(_ outbox.Entry) (int64, error) {
	return f.pos, f.err
}

// recordingApply records calls and returns an injected error.
type recordingApply struct {
	calls int
	err   error
}

func (r *recordingApply) fn(_ context.Context, _ outbox.Entry) error {
	r.calls++
	return r.err
}

// seededStore is a checkpoint store with pre-seeded offsets that records saves.
type seededStore struct {
	offsets   map[string]int64
	saveCalls int
	lastSaved int64
	loadErr   error
}

func newSeededStore(cellID, projectionID string, offset int64) *seededStore {
	s := &seededStore{offsets: make(map[string]int64)}
	if cellID != "" {
		s.offsets[checkpointKey(cellID, projectionID)] = offset
	}
	return s
}

func (s *seededStore) LoadOffset(_ context.Context, cellID, projectionID string) (int64, error) {
	if s.loadErr != nil {
		return 0, s.loadErr
	}
	return s.offsets[checkpointKey(cellID, projectionID)], nil
}

func (s *seededStore) SaveOffset(_ context.Context, cellID, projectionID string, offset int64) error {
	s.saveCalls++
	s.lastSaved = offset
	s.offsets[checkpointKey(cellID, projectionID)] = offset
	return nil
}

// errSaveStore is a checkpoint store where LoadOffset returns a pre-seeded value
// and SaveOffset returns an injected error without modifying state (models
// a failed write that never committed).
type errSaveStore struct {
	currentOffset int64
	saveErr       error
	saveCalls     int
}

func (s *errSaveStore) LoadOffset(_ context.Context, _, _ string) (int64, error) {
	return s.currentOffset, nil
}

func (s *errSaveStore) SaveOffset(_ context.Context, _, _ string, _ int64) error {
	s.saveCalls++
	return s.saveErr // does NOT update currentOffset — models "failed write"
}

// fakeRegistrar records Subscribe calls and mirrors RegistryRecorder.Subscribe
// validation so tests fail-close in the same way as production wiring.
// It embeds cell.Registrar (nil) — only Subscribe is implemented.
type fakeRegistrar struct {
	cell.Registrar // nil embedding; panics on any other method call

	subscribeCalls int
	lastSpec       contractspec.ContractSpec
	lastCG         string
	lastCell       string
	subscribeErr   error
}

func (f *fakeRegistrar) Subscribe(
	spec contractspec.ContractSpec,
	handler outbox.EntryHandler,
	consumerGroup string,
	cellID string,
	_ ...cell.SubscriptionOption,
) error {
	// Mirror RegistryRecorder.Subscribe validation (registry.go:468-488) so
	// tests fail-close identically to production — prevents spec-kind drift.
	if handler == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"fakeRegistrar Subscribe: handler must not be nil")
	}
	if consumerGroup == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"fakeRegistrar Subscribe: consumerGroup must not be empty")
	}
	if cellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"fakeRegistrar Subscribe: cellID must not be empty")
	}
	if spec.Kind != cellvocab.ContractEvent {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"fakeRegistrar Subscribe: spec.Kind must be \"event\"")
	}
	if spec.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"fakeRegistrar Subscribe: spec.Topic must not be empty")
	}
	f.subscribeCalls++
	f.lastSpec = spec
	f.lastCG = consumerGroup
	f.lastCell = cellID
	return f.subscribeErr
}

// spySpan records SetStatus calls for assertion in span status tests.
type spySpan struct {
	statusCode wrapper.StatusCode
	statusDesc string
	statusSet  bool
}

func (s *spySpan) SetAttributes(_ ...wrapper.Attr) {}
func (s *spySpan) RecordError(_ error)             {}
func (s *spySpan) End()                            {}
func (s *spySpan) SetStatus(code wrapper.StatusCode, desc string) {
	s.statusCode = code
	s.statusDesc = desc
	s.statusSet = true
}

// spyTracer returns the last created spySpan so tests can inspect SetStatus calls.
type spyTracer struct {
	last *spySpan
}

func (st *spyTracer) Start(ctx context.Context, _ string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	sp := &spySpan{}
	st.last = sp
	return ctx, sp
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// minimalSpec returns a valid event ContractSpec suitable for passing to
// Coordinator.Subscribe. Kind must be "event" (not "projection" — projection is
// the slice concept, not the kind of the subscribed event contract) and Topic
// must be non-empty to satisfy RegistryRecorder.Subscribe validation.
func minimalSpec(id string) contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:        id,
		Kind:      cellvocab.ContractEvent,
		Transport: "amqp",
		Topic:     "myproj.events.v1",
	}
}

func newCoordinator(t *testing.T, reg cell.Registrar, txr persistence.TxRunner, store CheckpointStore, cur Cursor) *Coordinator {
	t.Helper()
	clk := clockmock.New(time.Now())
	replay := NewMemReplaySource()
	c, err := NewCoordinator(clk, "testcell", "p1", reg, txr, store, cur, replay, wrapper.NoopTracer{}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// TestCoordinator_ApplyOne — direct applyOne / buildHandler tests
// ---------------------------------------------------------------------------

func TestCoordinator_ApplyOne(t *testing.T) {
	t.Parallel()

	errTransient := errors.New("transient")
	errPermWrap := outbox.NewPermanentError(errors.New("permanent"))
	errSave := errors.New("save failed")
	errCursor := errors.New("cursor error")

	tests := []applyOneCase{
		{
			name:           "cold-start: pos=1 applied and saved",
			currentOffset:  0,
			cursorPos:      1,
			wantApplyCalls: 1,
			wantSaved:      true,
			wantLastSaved:  1,
			wantDisp:       outbox.DispositionAck,
		},
		{
			name:           "crash-recovery: current=5 pos=6 applied",
			currentOffset:  5,
			cursorPos:      6,
			wantApplyCalls: 1,
			wantSaved:      true,
			wantLastSaved:  6,
			wantDisp:       outbox.DispositionAck,
		},
		{
			name:           "skip: current=5 pos=5",
			currentOffset:  5,
			cursorPos:      5,
			wantApplyCalls: 0,
			wantSaved:      false,
			wantDisp:       outbox.DispositionAck,
		},
		{
			name:           "out-of-order: current=5 pos=3",
			currentOffset:  5,
			cursorPos:      3,
			wantApplyCalls: 0,
			wantSaved:      false,
			wantDisp:       outbox.DispositionAck,
		},
		{
			// F2: enforce the Cursor 1-based invariant (cursor.go #2) at the trust
			// boundary. A non-conformant Cursor returning 0 for a real event must NOT
			// be silently Ack-skipped (0<=0 at cold start = a dropped event); it
			// routes to the DLX as a permanent error so the breach is observable.
			name:           "invalid: cursor pos=0 violates 1-based invariant → Reject",
			currentOffset:  0,
			cursorPos:      0,
			wantApplyCalls: 0,
			wantSaved:      false,
			wantDisp:       outbox.DispositionReject,
		},
		{
			name:           "apply transient error → Requeue",
			currentOffset:  0,
			cursorPos:      1,
			applyErr:       errTransient,
			wantApplyCalls: 1,
			wantSaved:      false,
			wantDisp:       outbox.DispositionRequeue,
		},
		{
			name:           "apply permanent error → Reject",
			currentOffset:  0,
			cursorPos:      1,
			applyErr:       errPermWrap,
			wantApplyCalls: 1,
			wantSaved:      false,
			wantDisp:       outbox.DispositionReject,
		},
		{
			// fail-closed: SaveOffset error keeps checkpoint at the old value.
			// The Coordinator requeues the event so it will be retried; the
			// read-model write (Apply) has already run, but without a committed
			// checkpoint the event is not considered "done" — on retry the tx
			// will roll back both Apply and SaveOffset together (atomic).
			// NOTE: Apply's effect on the read model is rolled back atomically
			// by the TxRunner only in real PG usage; mem fake + recordingApply
			// have no side-effects to roll back, so that invariant is covered
			// by PR-02 PG integration tests rather than here.
			name:           "fail-closed: SaveOffset error → Requeue, checkpoint not advanced",
			currentOffset:  0,
			cursorPos:      1,
			saveFails:      true,
			wantApplyCalls: 1,
			wantSaved:      true, // saveCalls incremented even though it fails
			wantDisp:       outbox.DispositionRequeue,
		},
		{
			name:           "cursor error → Requeue",
			currentOffset:  0,
			cursorErr:      errCursor,
			wantApplyCalls: 0,
			wantSaved:      false,
			wantDisp:       outbox.DispositionRequeue,
		},
		{
			// Gap: current=5, pos=7 — gap events 6 is simply never applied.
			// After pos=7 is applied and checkpoint advances to 7, a subsequent
			// event at pos=6 (≤7) will be skipped by the pos<=checkpoint guard.
			// This is expected: the Coordinator relies on Cursor monotonicity;
			// events at positions inside a gap are intentionally not retried.
			name:           "gap: current=5 pos=7 → applied and saved at 7",
			currentOffset:  5,
			cursorPos:      7,
			wantApplyCalls: 1,
			wantSaved:      true,
			wantLastSaved:  7,
			wantDisp:       outbox.DispositionAck,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runApplyOneCase(t, tc, errSave)
		})
	}

	// LoadOffset error sub-test (needs different store setup)
	t.Run("LoadOffset error → Requeue", func(t *testing.T) {
		t.Parallel()
		apply := &recordingApply{}
		cursor := &fakeCursor{pos: 1}
		txr := &fakeTxRunner{}
		reg := &fakeRegistrar{}
		store := &seededStore{offsets: make(map[string]int64), loadErr: errors.New("load failed")}

		clk := clockmock.New(time.Now())
		c, err := NewCoordinator(clk, "testcell", "p1", reg, txr, store, cursor, NewMemReplaySource(), wrapper.NoopTracer{}, nil)
		if err != nil {
			t.Fatalf("NewCoordinator: %v", err)
		}
		h := c.buildHandler(apply.fn)
		result := h(context.Background(), outbox.Entry{})

		if apply.calls != 0 {
			t.Errorf("apply.calls = %d, want 0", apply.calls)
		}
		if result.Disposition != outbox.DispositionRequeue {
			t.Errorf("Disposition = %v, want Requeue", result.Disposition)
		}
	})
}

// ---------------------------------------------------------------------------
// TestCoordinator_ReorderDropsLowerPosition — exactly-once ordering precondition
// ---------------------------------------------------------------------------

// TestCoordinator_ReorderDropsLowerPosition CHARACTERIZES the exactly-once
// precondition: the cumulative-watermark skip in applyOne (pos<=checkpoint →
// Ack-skip) is only sound under STRICTLY SERIAL, IN-ORDER delivery of a
// projection's stream. If a higher position commits the checkpoint before a lower
// position is processed — which happens under concurrent delivery (the production
// AMQP subscriber dispatches one goroutine per delivery with prefetch defaulting
// to 10) or broker redelivery-reorder — the lower position's distinct Apply is
// silently dropped (Acked, never applied), producing a projection gap.
//
// This test deterministically simulates that interleaving (pos=7 commits, then
// pos=6 arrives). It asserts the lower event IS dropped: this documents the HAZARD
// (why serial in-order delivery is required), not desired end-state behavior. v1
// ships safe because cmd/* wires only the serial in-memory bus and the production
// subscriber wiring (cellgen kind:projection) lands in PR-04 (#1176), which MUST
// enforce prefetch=1 / single-goroutine dispatch before a concurrent transport
// carries a projection subscription. See kernel/projection/doc.go "Ordering
// precondition" and ADR §6 threat row 4.
func TestCoordinator_ReorderDropsLowerPosition(t *testing.T) {
	t.Parallel()

	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}
	ctx := context.Background()

	// Higher position 7 is processed first → checkpoint advances to 7.
	applyHi := &recordingApply{}
	cHi := newCoordinator(t, reg, txr, store, &fakeCursor{pos: 7})
	rHi := cHi.buildHandler(applyHi.fn)(ctx, outbox.Entry{})
	if applyHi.calls != 1 || rHi.Disposition != outbox.DispositionAck {
		t.Fatalf("pos=7: calls=%d disp=%v, want 1 / Ack", applyHi.calls, rHi.Disposition)
	}

	// Lower position 6 arrives AFTER the checkpoint is at 7 → 6 <= 7 → silently
	// skipped. Apply for pos=6 never runs: this is the dropped distinct event.
	applyLo := &recordingApply{}
	cLo := newCoordinator(t, reg, txr, store, &fakeCursor{pos: 6})
	rLo := cLo.buildHandler(applyLo.fn)(ctx, outbox.Entry{})
	if applyLo.calls != 0 {
		t.Fatalf("pos=6 after checkpoint=7: Apply called %d times — hazard expects 0 (silently dropped)", applyLo.calls)
	}
	if rLo.Disposition != outbox.DispositionAck {
		t.Fatalf("pos=6 dropped-event disposition=%v, want Ack (silent skip)", rLo.Disposition)
	}

	off, _ := store.LoadOffset(ctx, "testcell", "p1")
	if off != 7 {
		t.Fatalf("checkpoint=%d, want 7 (a lower position must not regress the watermark)", off)
	}
}

// ---------------------------------------------------------------------------
// TestBuildHandler_SpanStatus — span SetStatus correctness (FIX-2)
// ---------------------------------------------------------------------------

// TestBuildHandler_SpanStatus verifies that buildHandler marks the "projection.apply"
// span status correctly for each disposition:
//
//   - success (nil err)        → SetStatus(StatusOK, "")
//   - transient error          → SetStatus(StatusError, "requeue")
//   - permanent error          → SetStatus(StatusError, "reject")
//
// Detailed error recording (RecordError) is delegated to the outer WrapConsumer
// span to avoid re-introducing a redaction funnel obligation here.
func TestBuildHandler_SpanStatus(t *testing.T) {
	t.Parallel()

	errTransient := errors.New("transient")
	errPermanent := outbox.NewPermanentError(errors.New("permanent"))

	tests := []spanStatusCase{
		{
			name:     "success → StatusOK",
			applyErr: nil,
			wantCode: wrapper.StatusOK,
			wantDesc: "",
		},
		{
			name:     "transient error → StatusError requeue",
			applyErr: errTransient,
			wantCode: wrapper.StatusError,
			wantDesc: "requeue",
		},
		{
			name:     "permanent error → StatusError reject",
			applyErr: errPermanent,
			wantCode: wrapper.StatusError,
			wantDesc: "reject",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runSpanStatusCase(t, tc)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCoordinator_CrashRecovery — same-process offset handoff via shared store
// ---------------------------------------------------------------------------

// TestCoordinator_CrashRecovery verifies that a second Coordinator pointed at
// the same in-memory store correctly reads the checkpoint written by the first
// and skips already-applied events. This models same-process offset handoff
// (e.g. a coordinator restart within the same test run sharing a MemCheckpointStore).
//
// True cross-process / cross-pod crash recovery (where the checkpoint is durably
// persisted to Postgres and a newly-started pod reads it back) is exercised by
// PR-02 PG integration tests — MemCheckpointStore has no persistence across
// process boundaries.
func TestCoordinator_CrashRecovery(t *testing.T) {
	t.Parallel()

	// Shared in-memory store: both coordinators share the same pointer.
	// This simulates same-process checkpoint handoff, not true crash recovery.
	store := NewMemCheckpointStore()
	cursor := &fakeCursor{pos: 6}
	apply := &recordingApply{}
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}

	// First Coordinator processes pos=6 and saves checkpoint.
	c1 := newCoordinator(t, reg, txr, store, cursor)
	h1 := c1.buildHandler(apply.fn)
	h1(context.Background(), outbox.Entry{})

	offset, _ := store.LoadOffset(context.Background(), "testcell", "p1")
	if offset != 6 {
		t.Fatalf("after first apply: checkpoint = %d, want 6", offset)
	}

	// Second Coordinator (simulates crash-restart) processes the same event.
	// pos=6 == current=6 → skip.
	apply2 := &recordingApply{}
	c2 := newCoordinator(t, &fakeRegistrar{}, &fakeTxRunner{}, store, &fakeCursor{pos: 6})
	h2 := c2.buildHandler(apply2.fn)
	r2 := h2(context.Background(), outbox.Entry{})
	if apply2.calls != 0 {
		t.Errorf("second coordinator: apply called %d times on duplicate pos, want 0", apply2.calls)
	}
	if r2.Disposition != outbox.DispositionAck {
		t.Errorf("second coordinator duplicate: Disposition = %v, want Ack", r2.Disposition)
	}

	// Second Coordinator advances to pos=7.
	apply3 := &recordingApply{}
	c3 := newCoordinator(t, &fakeRegistrar{}, &fakeTxRunner{}, store, &fakeCursor{pos: 7})
	h3 := c3.buildHandler(apply3.fn)
	r3 := h3(context.Background(), outbox.Entry{})
	if apply3.calls != 1 {
		t.Errorf("third coordinator: apply.calls = %d, want 1", apply3.calls)
	}
	offset2, _ := store.LoadOffset(context.Background(), "testcell", "p1")
	if offset2 != 7 {
		t.Errorf("after third apply: checkpoint = %d, want 7", offset2)
	}
	if r3.Disposition != outbox.DispositionAck {
		t.Errorf("third coordinator: Disposition = %v, want Ack", r3.Disposition)
	}
}

// ---------------------------------------------------------------------------
// TestCoordinator_GapSkip — forward-gap position handling (FIX-3)
// ---------------------------------------------------------------------------

// TestCoordinator_GapSkip proves that after a gap (current=5 → apply pos=7),
// a later event at pos=6 (now ≤ checkpoint=7) is skipped with Ack and Apply is
// not called. This is the correct behavior: the Coordinator relies on Cursor
// monotonicity; gap-interior events arriving late are silently consumed.
func TestCoordinator_GapSkip(t *testing.T) {
	t.Parallel()

	store := NewMemCheckpointStore()
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}

	// Seed checkpoint at 5.
	if err := store.SaveOffset(context.Background(), "testcell", "p1", 5); err != nil {
		t.Fatalf("seed SaveOffset: %v", err)
	}

	// Apply pos=7 (gap over 6).
	applyFirst := &recordingApply{}
	c1 := newCoordinator(t, reg, txr, store, &fakeCursor{pos: 7})
	h1 := c1.buildHandler(applyFirst.fn)
	r1 := h1(context.Background(), outbox.Entry{})

	if applyFirst.calls != 1 {
		t.Errorf("first apply: calls = %d, want 1", applyFirst.calls)
	}
	if r1.Disposition != outbox.DispositionAck {
		t.Errorf("first apply: Disposition = %v, want Ack", r1.Disposition)
	}
	if off, _ := store.LoadOffset(context.Background(), "testcell", "p1"); off != 7 {
		t.Errorf("checkpoint after pos=7: got %d, want 7", off)
	}

	// Now deliver pos=6 — it is ≤ checkpoint=7, so it must be skipped.
	// This is expected behavior: pos=6 arrived inside a gap that was already
	// passed by the monotonically-advancing cursor. Apply must NOT be called.
	applyLate := &recordingApply{}
	c2 := newCoordinator(t, &fakeRegistrar{}, txr, store, &fakeCursor{pos: 6})
	h2 := c2.buildHandler(applyLate.fn)
	r2 := h2(context.Background(), outbox.Entry{})

	if applyLate.calls != 0 {
		t.Errorf("late gap event pos=6: apply.calls = %d, want 0 (must be skipped — pos ≤ checkpoint=7)", applyLate.calls)
	}
	if r2.Disposition != outbox.DispositionAck {
		t.Errorf("late gap event pos=6: Disposition = %v, want Ack", r2.Disposition)
	}
}

// ---------------------------------------------------------------------------
// TestNewCoordinator_NilGuards
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilGuards(t *testing.T) {
	t.Parallel()

	validReg := &fakeRegistrar{}
	validTxr := &fakeTxRunner{}
	validStore := NewMemCheckpointStore()
	validCursor := &fakeCursor{}
	var validTracer wrapper.Tracer = wrapper.NoopTracer{}

	// Typed-nil for store (must be rejected like bare-nil).
	var typedNilStore *MemCheckpointStore

	tests := []nilGuardCase{
		{
			name:    "all valid",
			cellID:  "testcell",
			reg:     validReg,
			txr:     validTxr,
			store:   validStore,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: false,
		},
		{
			name:    "empty cellID",
			cellID:  "",
			reg:     validReg,
			txr:     validTxr,
			store:   validStore,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "nil reg",
			cellID:  "testcell",
			reg:     nil,
			txr:     validTxr,
			store:   validStore,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "nil txRunner",
			cellID:  "testcell",
			reg:     validReg,
			txr:     nil,
			store:   validStore,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "nil store",
			cellID:  "testcell",
			reg:     validReg,
			txr:     validTxr,
			store:   nil,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "typed-nil store",
			cellID:  "testcell",
			reg:     validReg,
			txr:     validTxr,
			store:   typedNilStore,
			cursor:  validCursor,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "nil cursor",
			cellID:  "testcell",
			reg:     validReg,
			txr:     validTxr,
			store:   validStore,
			cursor:  nil,
			tracer:  validTracer,
			wantErr: true,
		},
		{
			name:    "nil tracer",
			cellID:  "testcell",
			reg:     validReg,
			txr:     validTxr,
			store:   validStore,
			cursor:  validCursor,
			tracer:  nil,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runNilGuardCase(t, tc)
		})
	}
}

// ---------------------------------------------------------------------------
// TestCoordinator_Subscribe
// ---------------------------------------------------------------------------

func TestCoordinator_Subscribe(t *testing.T) {
	t.Parallel()

	errRegister := errors.New("register failed")

	tests := []subscribeCase{
		{
			name:         "happy path",
			projectionID: "myproj",
			applyFn:      func(_ context.Context, _ outbox.Entry) error { return nil },
			wantErr:      false,
			wantSubCalls: 1,
			wantCG:       "testcell-myproj",
			wantCellID:   "testcell",
		},
		{
			name:         "empty projectionID",
			projectionID: "",
			applyFn:      func(_ context.Context, _ outbox.Entry) error { return nil },
			wantErr:      true,
			wantSubCalls: 0,
		},
		{
			name:         "nil apply",
			projectionID: "myproj",
			applyFn:      nil,
			wantErr:      true,
			wantSubCalls: 0,
		},
		{
			name:         "reg.Subscribe returns error",
			projectionID: "myproj",
			applyFn:      func(_ context.Context, _ outbox.Entry) error { return nil },
			regErr:       errRegister,
			wantErr:      true,
			wantSubCalls: 1,
		},
	}

	// Negative test (F4): a non-event spec (kind:projection) must be rejected by
	// Coordinator.Subscribe's own fail-fast kind guard, BEFORE reaching the
	// registrar. ContractSpec.Validate() permits kind:projection without a Topic,
	// so without the guard the rejection would only come from reg.Subscribe; the
	// guard makes the documented "spec.Kind must be event" precondition
	// self-enforcing (same trust-boundary principle as the pos<1 guard in applyOne).
	// The fakeRegistrar mirror is defense-in-depth, unreached here.
	t.Run("non-event spec rejected by Subscribe kind guard", func(t *testing.T) {
		t.Parallel()
		reg := &fakeRegistrar{}
		clk := clockmock.New(time.Now())
		c, err := NewCoordinator(
			clk, "testcell", "myproj",
			reg, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{},
			NewMemReplaySource(), wrapper.NoopTracer{}, nil,
		)
		if err != nil {
			t.Fatalf("NewCoordinator: %v", err)
		}
		badSpec := contractspec.ContractSpec{
			ID:        "projection.myproj.v1",
			Kind:      cellvocab.ContractProjection, // wrong: must be event
			Transport: "internal",
			// Topic intentionally absent
		}
		subscribeErr := c.Subscribe(context.Background(), badSpec,
			func(_ context.Context, _ outbox.Entry) error { return nil },
		)
		if subscribeErr == nil {
			t.Fatal("expected error for non-event spec, got nil")
		}
		var ec *errcode.Error
		if !errors.As(subscribeErr, &ec) || ec.Kind != errcode.KindInvalid {
			t.Errorf("error = %v, want *errcode.Error KindInvalid", subscribeErr)
		}
		// The guard fired (its message), not the registrar mirror, and short-circuited
		// before the registrar was ever called.
		if !strings.Contains(subscribeErr.Error(), "projection.Subscribe: spec.Kind") {
			t.Errorf("error %q, want the projection.Subscribe kind-guard message (not the registrar's)", subscribeErr)
		}
		if reg.subscribeCalls != 0 {
			t.Errorf("subscribeCalls = %d, want 0 (kind guard must reject before reaching the registrar)", reg.subscribeCalls)
		}
	})

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runSubscribeCase(t, tc)
		})
	}
}

// ---------------------------------------------------------------------------
// TestClassify
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	t.Parallel()

	errPlain := errors.New("plain")
	errPermanent := outbox.NewPermanentError(errors.New("perm"))
	errWrappedPermanent := errors.New("wrapped: " + errPermanent.Error())
	// Proper wrap using fmt.Errorf %w so errors.As can unwrap.
	errWrappedProper := func() error {
		return errors.Join(errors.New("outer"), outbox.NewPermanentError(errors.New("inner")))
	}()

	tests := []struct {
		name     string
		err      error
		wantDisp outbox.Disposition
	}{
		{"nil → Ack", nil, outbox.DispositionAck},
		{"plain error → Requeue", errPlain, outbox.DispositionRequeue},
		{"*PermanentError → Reject", errPermanent, outbox.DispositionReject},
		{"wrapped permanent (errors.Join) → Reject", errWrappedProper, outbox.DispositionReject},
		{"non-wrapped permanent string → Requeue", errWrappedPermanent, outbox.DispositionRequeue},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := classify(tc.err)
			if result.Disposition != tc.wantDisp {
				t.Errorf("classify(%v): Disposition = %v, want %v", tc.err, result.Disposition, tc.wantDisp)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestIsPermanent
// ---------------------------------------------------------------------------

func TestIsPermanent(t *testing.T) {
	t.Parallel()

	errInner := errors.New("inner")
	errPerm := outbox.NewPermanentError(errInner)
	errWrapped := errors.Join(errors.New("outer"), errPerm)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("x"), false},
		{"*PermanentError direct", errPerm, true},
		{"*PermanentError wrapped via errors.Join", errWrapped, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPermanent(tc.err); got != tc.want {
				t.Errorf("isPermanent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCheckpointKey — internal helper (accessible from package projection)
// ---------------------------------------------------------------------------

func TestCheckpointKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cell, proj string
		want       string
	}{
		{"cellA", "p1", "cellA/p1"},
		{"", "p1", "/p1"},
		{"cellA", "", "cellA/"},
	}
	for _, tc := range cases {
		got := checkpointKey(tc.cell, tc.proj)
		if got != tc.want {
			t.Errorf("checkpointKey(%q, %q) = %q, want %q", tc.cell, tc.proj, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestPhase_AlwaysLive
// ---------------------------------------------------------------------------

func TestPhase_AlwaysLive(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	c, err := NewCoordinator(
		clk,
		"testcell", "p1",
		&fakeRegistrar{},
		&fakeTxRunner{},
		NewMemCheckpointStore(),
		&fakeCursor{},
		NewMemReplaySource(),
		wrapper.NoopTracer{},
		nil,
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if got := c.Phase(); got != PhaseLive {
		t.Errorf("Phase() = %v, want PhaseLive", got)
	}
}

// ---------------------------------------------------------------------------
// runApplyOneCase — per-case executor for TestCoordinator_ApplyOne
// ---------------------------------------------------------------------------

type applyOneCase struct {
	name           string
	currentOffset  int64
	cursorPos      int64
	cursorErr      error
	applyErr       error
	saveFails      bool
	wantApplyCalls int
	wantSaved      bool
	wantLastSaved  int64
	wantDisp       outbox.Disposition
	wantLoadErr    bool
}

func runApplyOneCase(t *testing.T, tc applyOneCase, errSave error) {
	t.Helper()

	apply := &recordingApply{err: tc.applyErr}
	cursor := &fakeCursor{pos: tc.cursorPos, err: tc.cursorErr}
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}

	var store CheckpointStore
	switch {
	case tc.wantLoadErr:
		store = &seededStore{offsets: make(map[string]int64), loadErr: errors.New("load failed")}
	case tc.saveFails:
		store = &errSaveStore{currentOffset: tc.currentOffset, saveErr: errSave}
	default:
		store = newSeededStore("testcell", "p1", tc.currentOffset)
	}

	clk := clockmock.New(time.Now())
	c, err := NewCoordinator(clk, "testcell", "p1", reg, txr, store, cursor, NewMemReplaySource(), wrapper.NoopTracer{}, nil)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	h := c.buildHandler(apply.fn)
	result := h(context.Background(), outbox.Entry{})

	if apply.calls != tc.wantApplyCalls {
		t.Errorf("apply.calls = %d, want %d", apply.calls, tc.wantApplyCalls)
	}

	assertApplyOneSaveState(t, tc, store)

	if result.Disposition != tc.wantDisp {
		t.Errorf("Disposition = %v, want %v", result.Disposition, tc.wantDisp)
	}
}

func assertApplyOneSaveState(t *testing.T, tc applyOneCase, store CheckpointStore) {
	t.Helper()
	if tc.saveFails {
		es := store.(*errSaveStore)
		if es.saveCalls == 0 {
			t.Error("expected errSaveStore.SaveOffset to be called")
		}
		// After a save error the persisted value must not have changed.
		got, _ := store.LoadOffset(context.Background(), "testcell", "p1")
		if got != tc.currentOffset {
			t.Errorf("after save error LoadOffset = %d, want original %d", got, tc.currentOffset)
		}
		return
	}
	ss, ok := store.(*seededStore)
	if !ok {
		return
	}
	if tc.wantSaved && ss.saveCalls == 0 {
		t.Error("expected SaveOffset to be called, but it was not")
	}
	if !tc.wantSaved && ss.saveCalls != 0 {
		t.Errorf("expected no SaveOffset calls, but got %d", ss.saveCalls)
	}
	if tc.wantSaved && ss.lastSaved != tc.wantLastSaved {
		t.Errorf("lastSaved = %d, want %d", ss.lastSaved, tc.wantLastSaved)
	}
}

// ---------------------------------------------------------------------------
// runNilGuardCase — per-case executor for TestNewCoordinator_NilGuards
// ---------------------------------------------------------------------------

type nilGuardCase struct {
	name    string
	cellID  string
	reg     cell.Registrar
	txr     persistence.TxRunner
	store   CheckpointStore
	cursor  Cursor
	tracer  wrapper.Tracer
	wantErr bool
}

func runNilGuardCase(t *testing.T, tc nilGuardCase) {
	t.Helper()
	clk := clockmock.New(time.Now())
	c, err := NewCoordinator(clk, tc.cellID, "p1", tc.reg, tc.txr, tc.store, tc.cursor, NewMemReplaySource(), tc.tracer, nil)
	if !tc.wantErr {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil coordinator")
		}
		return
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var e *errcode.Error
	if !errors.As(err, &e) {
		t.Fatalf("error is not *errcode.Error: %T %v", err, err)
	}
	if e.Kind != errcode.KindInvalid {
		t.Errorf("Kind = %v, want KindInvalid", e.Kind)
	}
	if c != nil {
		t.Error("expected nil coordinator on error")
	}
}

// ---------------------------------------------------------------------------
// runSubscribeCase — per-case executor for TestCoordinator_Subscribe
// ---------------------------------------------------------------------------

type subscribeCase struct {
	name         string
	projectionID string
	applyFn      Apply
	regErr       error
	wantErr      bool
	wantSubCalls int
	wantCG       string
	wantCellID   string
}

func runSubscribeCase(t *testing.T, tc subscribeCase) {
	t.Helper()
	// In PR-03, projectionID is in NewCoordinator, not Subscribe. The "empty
	// projectionID" case is now a NewCoordinator validation; we thread it through
	// the constructor instead of Subscribe.
	projID := tc.projectionID
	if projID == "" {
		// Test case wants an error from empty projectionID — NewCoordinator rejects.
		clk := clockmock.New(time.Now())
		_, err := NewCoordinator(
			clk, "testcell", "",
			&fakeRegistrar{}, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{},
			NewMemReplaySource(), wrapper.NoopTracer{}, nil,
		)
		if tc.wantErr && err == nil {
			t.Fatal("expected error for empty projectionID from NewCoordinator, got nil")
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected NewCoordinator error: %v", err)
		}
		return
	}

	reg := &fakeRegistrar{subscribeErr: tc.regErr}
	clk := clockmock.New(time.Now())
	c, err := NewCoordinator(
		clk, "testcell", projID,
		reg, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{},
		NewMemReplaySource(), wrapper.NoopTracer{}, nil,
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	spec := minimalSpec("projection.myproj.v1")
	subscribeErr := c.Subscribe(context.Background(), spec, tc.applyFn)

	if tc.wantErr && subscribeErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !tc.wantErr && subscribeErr != nil {
		t.Fatalf("unexpected error: %v", subscribeErr)
	}

	if reg.subscribeCalls != tc.wantSubCalls {
		t.Errorf("subscribeCalls = %d, want %d", reg.subscribeCalls, tc.wantSubCalls)
	}
	if !tc.wantErr && tc.wantCG != "" {
		if reg.lastCG != tc.wantCG {
			t.Errorf("consumerGroup = %q, want %q", reg.lastCG, tc.wantCG)
		}
		if reg.lastCell != tc.wantCellID {
			t.Errorf("cellID = %q, want %q", reg.lastCell, tc.wantCellID)
		}
	}
}

// ---------------------------------------------------------------------------
// runSpanStatusCase — per-case executor for TestBuildHandler_SpanStatus
// ---------------------------------------------------------------------------

type spanStatusCase struct {
	name     string
	applyErr error
	wantCode wrapper.StatusCode
	wantDesc string
}

func runSpanStatusCase(t *testing.T, tc spanStatusCase) {
	t.Helper()
	spy := &spyTracer{}
	apply := &recordingApply{err: tc.applyErr}
	store := newSeededStore("testcell", "p1", 0)
	cursor := &fakeCursor{pos: 1}

	clk := clockmock.New(time.Now())
	c, err := NewCoordinator(clk, "testcell", "p1", &fakeRegistrar{}, &fakeTxRunner{}, store, cursor, NewMemReplaySource(), spy, nil)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	h := c.buildHandler(apply.fn)
	_ = h(context.Background(), outbox.Entry{})

	if spy.last == nil {
		t.Fatal("spy tracer: no span was started")
	}
	if !spy.last.statusSet {
		t.Fatal("span.SetStatus was never called")
	}
	if spy.last.statusCode != tc.wantCode {
		t.Errorf("SetStatus code = %v, want %v", spy.last.statusCode, tc.wantCode)
	}
	if spy.last.statusDesc != tc.wantDesc {
		t.Errorf("SetStatus desc = %q, want %q", spy.last.statusDesc, tc.wantDesc)
	}
}
