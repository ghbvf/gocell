package projection

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
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

// fakeRegistrar records Subscribe calls and returns an injected error.
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
	_ outbox.EntryHandler,
	consumerGroup string,
	cellID string,
	_ ...cell.SubscriptionOption,
) error {
	f.subscribeCalls++
	f.lastSpec = spec
	f.lastCG = consumerGroup
	f.lastCell = cellID
	return f.subscribeErr
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func minimalSpec(id string) contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:        id,
		Kind:      cellvocab.ContractProjection,
		Transport: "internal",
	}
}

func newCoordinator(t *testing.T, reg cell.Registrar, txr persistence.TxRunner, store CheckpointStore, cur Cursor) *Coordinator {
	t.Helper()
	c, err := NewCoordinator("testcell", reg, txr, store, cur, wrapper.NoopTracer{})
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

	tests := []struct {
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
	}{
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
			name:           "SaveOffset error → Requeue, LoadOffset still old",
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
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			apply := &recordingApply{err: tc.applyErr}
			cursor := &fakeCursor{pos: tc.cursorPos, err: tc.cursorErr}
			txr := &fakeTxRunner{}
			reg := &fakeRegistrar{}

			var store CheckpointStore
			if tc.wantLoadErr {
				store = &seededStore{offsets: make(map[string]int64), loadErr: errors.New("load failed")}
			} else if tc.saveFails {
				store = &errSaveStore{currentOffset: tc.currentOffset, saveErr: errSave}
			} else {
				store = newSeededStore("testcell", "p1", tc.currentOffset)
			}

			c, err := NewCoordinator("testcell", reg, txr, store, cursor, wrapper.NoopTracer{})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}

			h := c.buildHandler("testcell", "p1", apply.fn)
			result := h(context.Background(), outbox.Entry{})

			if apply.calls != tc.wantApplyCalls {
				t.Errorf("apply.calls = %d, want %d", apply.calls, tc.wantApplyCalls)
			}

			if !tc.saveFails {
				ss, ok := store.(*seededStore)
				if ok {
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
			} else {
				es := store.(*errSaveStore)
				if es.saveCalls == 0 {
					t.Error("expected errSaveStore.SaveOffset to be called")
				}
				// After a save error the persisted value must not have changed.
				got, _ := store.LoadOffset(context.Background(), "testcell", "p1")
				if got != tc.currentOffset {
					t.Errorf("after save error LoadOffset = %d, want original %d", got, tc.currentOffset)
				}
			}

			if result.Disposition != tc.wantDisp {
				t.Errorf("Disposition = %v, want %v", result.Disposition, tc.wantDisp)
			}
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

		c, err := NewCoordinator("testcell", reg, txr, store, cursor, wrapper.NoopTracer{})
		if err != nil {
			t.Fatalf("NewCoordinator: %v", err)
		}
		h := c.buildHandler("testcell", "p1", apply.fn)
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
// TestCoordinator_CrashRecovery — simulates restart with a shared store
// ---------------------------------------------------------------------------

func TestCoordinator_CrashRecovery(t *testing.T) {
	t.Parallel()

	// Shared persistent store, simulating a process restart by creating a
	// second Coordinator pointing at the same store.
	store := NewMemCheckpointStore()
	cursor := &fakeCursor{pos: 6}
	apply := &recordingApply{}
	txr := &fakeTxRunner{}
	reg := &fakeRegistrar{}

	// First Coordinator processes pos=6 and saves checkpoint.
	c1 := newCoordinator(t, reg, txr, store, cursor)
	h1 := c1.buildHandler("testcell", "p1", apply.fn)
	h1(context.Background(), outbox.Entry{})

	offset, _ := store.LoadOffset(context.Background(), "testcell", "p1")
	if offset != 6 {
		t.Fatalf("after first apply: checkpoint = %d, want 6", offset)
	}

	// Second Coordinator (simulates crash-restart) processes the same event.
	// pos=6 == current=6 → skip.
	apply2 := &recordingApply{}
	c2 := newCoordinator(t, &fakeRegistrar{}, &fakeTxRunner{}, store, &fakeCursor{pos: 6})
	h2 := c2.buildHandler("testcell", "p1", apply2.fn)
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
	h3 := c3.buildHandler("testcell", "p1", apply3.fn)
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

	tests := []struct {
		name    string
		cellID  string
		reg     cell.Registrar
		txr     persistence.TxRunner
		store   CheckpointStore
		cursor  Cursor
		tracer  wrapper.Tracer
		wantErr bool
	}{
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
			c, err := NewCoordinator(tc.cellID, tc.reg, tc.txr, tc.store, tc.cursor, tc.tracer)
			if tc.wantErr {
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
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c == nil {
					t.Fatal("expected non-nil coordinator")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestCoordinator_Subscribe
// ---------------------------------------------------------------------------

func TestCoordinator_Subscribe(t *testing.T) {
	t.Parallel()

	errRegister := errors.New("register failed")

	tests := []struct {
		name         string
		projectionID string
		applyFn      Apply
		regErr       error
		wantErr      bool
		wantSubCalls int
		wantCG       string
		wantCellID   string
	}{
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

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := &fakeRegistrar{subscribeErr: tc.regErr}
			c, err := NewCoordinator("testcell", reg, &fakeTxRunner{}, NewMemCheckpointStore(), &fakeCursor{}, wrapper.NoopTracer{})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}

			spec := minimalSpec("projection.myproj.v1")
			subscribeErr := c.Subscribe(context.Background(), spec, tc.projectionID, tc.applyFn)

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
	c, err := NewCoordinator(
		"testcell",
		&fakeRegistrar{},
		&fakeTxRunner{},
		NewMemCheckpointStore(),
		&fakeCursor{},
		wrapper.NoopTracer{},
	)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if got := c.Phase(); got != PhaseLive {
		t.Errorf("Phase() = %v, want PhaseLive", got)
	}
}
