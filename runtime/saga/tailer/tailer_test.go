package tailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

const (
	testCell = "auditcore"
	testProj = "sagastatus"

	// testLockTTL is the distlock TTL used when a test competitor holds the
	// per-projection lock (TEST-TIME-LITERAL-01: durations are package consts).
	testLockTTL = 30 * time.Second
	// testLifecycleTimeout bounds Start/Stop lifecycle waits in tests.
	testLifecycleTimeout = 2 * time.Second
)

// ── fakes ─────────────────────────────────────────────────────────────────

// fakeEvent implements projection.ProjectionEvent with an explicit GlobalSeq.
type fakeEvent struct {
	seq int64
	id  string
}

func (e *fakeEvent) EventID() string                                    { return e.id }
func (e *fakeEvent) Payload() []byte                                    { return []byte(`{}`) }
func (e *fakeEvent) OccurredAt() time.Time                              { return time.Unix(e.seq, 0) }
func (e *fakeEvent) Stream() string                                     { return "saga.journal.v1" }
func (e *fakeEvent) RestoreContext(ctx context.Context) context.Context { return ctx }

// fakeSource implements both projection.ReplaySource and projection.Cursor over
// an in-memory ordered event slice (mirrors sagaprojection.SagaJournalSource).
type fakeSource struct {
	events  []*fakeEvent // sorted ascending by seq
	headErr error
}

func (s *fakeSource) Head(context.Context) (int64, error) {
	if s.headErr != nil {
		return 0, s.headErr
	}
	if len(s.events) == 0 {
		return 0, nil
	}
	return s.events[len(s.events)-1].seq, nil
}

func (s *fakeSource) Replay(ctx context.Context, fromOffset int64, fn func(projection.ProjectionEvent) error) error {
	for _, e := range s.events {
		if e.seq <= fromOffset {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeSource) Position(evt projection.ProjectionEvent) (int64, error) {
	fe, ok := evt.(*fakeEvent)
	if !ok {
		return 0, errcode.New(errcode.KindInternal, errcode.ErrInternal, "fakeSource: not a fakeEvent")
	}
	return fe.seq, nil
}

var (
	_ projection.ReplaySource = (*fakeSource)(nil)
	_ projection.Cursor       = (*fakeSource)(nil)
)

// fakeOwnerStore embeds the real mem store but allows injecting LoadOffset and
// AdvanceIfOwner faults to exercise the error branches.
type fakeOwnerStore struct {
	*projection.MemOwnerCheckpointStore
	loadErr    error
	advanceErr error
}

func newFakeOwnerStore() *fakeOwnerStore {
	return &fakeOwnerStore{MemOwnerCheckpointStore: projection.NewMemOwnerCheckpointStore()}
}

func (f *fakeOwnerStore) LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error) {
	if f.loadErr != nil {
		return 0, f.loadErr
	}
	return f.MemOwnerCheckpointStore.LoadOffset(ctx, cellID, projectionID)
}

func (f *fakeOwnerStore) AdvanceIfOwner(ctx context.Context, cellID, projectionID, ownerToken string, offset int64) error {
	if f.advanceErr != nil {
		return f.advanceErr
	}
	return f.MemOwnerCheckpointStore.AdvanceIfOwner(ctx, cellID, projectionID, ownerToken, offset)
}

var _ projection.OwnerCheckpointStore = (*fakeOwnerStore)(nil)

// fakeTxRunner is a pass-through TxRunner: RunInTx runs fn(ctx) and propagates
// its error (no real rollback — the mem store mutates directly; the tx boundary
// is structural here, exercised for real in PG integration per ADR §9 PR-PG).
type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// recordingObserver captures observer calls for assertions.
type recordingObserver struct {
	mu        sync.Mutex
	lockSkips []LockAcquireResult
	drains    []DrainResult
	advances  []AdvanceResult
	lags      []int64
	lastOK    int
}

func (o *recordingObserver) ObserveLockAcquire(_ context.Context, _ string, r LockAcquireResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lockSkips = append(o.lockSkips, r)
}

func (o *recordingObserver) ObserveDrain(_ context.Context, _ string, r DrainResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.drains = append(o.drains, r)
}

func (o *recordingObserver) ObserveCheckpointAdvance(_ context.Context, _ string, r AdvanceResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.advances = append(o.advances, r)
}

func (o *recordingObserver) ObserveLag(_ context.Context, _ string, pending int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lags = append(o.lags, pending)
}

func (o *recordingObserver) ObserveLastSuccess(_ context.Context, _ string, _ time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastOK++
}

var _ Observer = (*recordingObserver)(nil)

func (o *recordingObserver) snapshot() ([]LockAcquireResult, []DrainResult, []AdvanceResult, []int64, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]LockAcquireResult(nil), o.lockSkips...),
		append([]DrainResult(nil), o.drains...),
		append([]AdvanceResult(nil), o.advances...),
		append([]int64(nil), o.lags...),
		o.lastOK
}

// ── helpers ───────────────────────────────────────────────────────────────

func newTestLocker(t *testing.T, clk clock.Clock) distlock.Locker {
	t.Helper()
	locker, err := distlock.New(locktest.NewFakeDriver(), clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	return locker
}

func newTestTailer(t *testing.T, src *fakeSource, store projection.OwnerCheckpointStore,
	apply projection.Apply, obs Observer, locker distlock.Locker, clk clock.Clock,
) *Tailer {
	t.Helper()
	tl, err := NewTailer(clk, src, src, store, fakeTxRunner{}, apply, locker, testCell, testProj,
		WithObserver(obs))
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	return tl
}

func events(seqs ...int64) []*fakeEvent {
	out := make([]*fakeEvent, len(seqs))
	for i, s := range seqs {
		out[i] = &fakeEvent{seq: s, id: fmt.Sprintf("evt-%d", s)}
	}
	return out
}

// ── tests ─────────────────────────────────────────────────────────────────

func TestTailer_DrainHappyPath(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1, 2, 3)}
	store := projection.NewMemOwnerCheckpointStore()
	var applied []string
	apply := func(_ context.Context, evt projection.ProjectionEvent) error {
		applied = append(applied, evt.EventID())
		return nil
	}
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, apply, obs, newTestLocker(t, clk), clk)

	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if want := []string{"evt-1", "evt-2", "evt-3"}; fmt.Sprint(applied) != fmt.Sprint(want) {
		t.Errorf("applied = %v, want %v", applied, want)
	}
	off, _ := store.LoadOffset(context.Background(), testCell, testProj)
	if off != 3 {
		t.Errorf("checkpoint = %d, want 3", off)
	}
	_, drains, advances, lags, lastOK := obs.snapshot()
	if len(advances) != 3 || advances[0] != AdvanceOK {
		t.Errorf("advances = %v, want 3×ok", advances)
	}
	if len(drains) != 1 || drains[0] != DrainOK {
		t.Errorf("drains = %v, want [ok]", drains)
	}
	if lastOK != 1 {
		t.Errorf("lastSuccess count = %d, want 1", lastOK)
	}
	if len(lags) != 1 || lags[0] != 0 {
		t.Errorf("lags = %v, want [0] (caught up)", lags)
	}
	if tl.lastSuccessUnixNano.Load() != clk.Now().UnixNano() {
		t.Errorf("lastSuccessUnixNano not stamped")
	}
}

func TestTailer_CaughtUpNoop(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1, 2)}
	store := projection.NewMemOwnerCheckpointStore()
	// Pre-advance checkpoint to head: nothing to drain.
	if err := store.AdvanceIfOwner(context.Background(), testCell, testProj, "seed", 2); err != nil {
		t.Fatalf("seed: %v", err)
	}
	obs := &recordingObserver{}
	applied := 0
	apply := func(context.Context, projection.ProjectionEvent) error { applied++; return nil }
	tl := newTestTailer(t, src, store, apply, obs, newTestLocker(t, clk), clk)

	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (caught up)", applied)
	}
	_, drains, _, _, lastOK := obs.snapshot()
	if len(drains) != 0 {
		t.Errorf("drains = %v, want none (idle caught-up tick not reported)", drains)
	}
	if lastOK != 1 {
		t.Errorf("lastSuccess = %d, want 1 (a clean tick is still a success)", lastOK)
	}
}

func TestTailer_LockContentionSkips(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	locker := newTestLocker(t, clk)
	src := &fakeSource{events: events(1, 2, 3)}
	store := projection.NewMemOwnerCheckpointStore()
	applied := 0
	apply := func(context.Context, projection.ProjectionEvent) error { applied++; return nil }
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, apply, obs, locker, clk)

	// A competitor holds the tailer's per-projection lock.
	held, err := locker.Acquire(context.Background(), tailerLockKey(testProj), testLockTTL)
	if err != nil {
		t.Fatalf("competitor acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce should skip cleanly, got: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (no drain without lock)", applied)
	}
	lockSkips, _, _, _, _ := obs.snapshot()
	if len(lockSkips) != 1 || lockSkips[0] != LockContended {
		t.Errorf("lockSkips = %v, want [contended]", lockSkips)
	}
}

func TestTailer_DrainStoreError(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	store := newFakeOwnerStore()
	store.loadErr = errors.New("checkpoint store down")
	src := &fakeSource{events: events(1)}
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, func(context.Context, projection.ProjectionEvent) error { return nil }, obs, newTestLocker(t, clk), clk)

	err := tl.pollOnce(context.Background())
	if err == nil {
		t.Fatal("pollOnce: want error from store load failure")
	}
	_, drains, _, _, lastOK := obs.snapshot()
	if len(drains) != 1 || drains[0] != DrainStoreError {
		t.Errorf("drains = %v, want [store_error]", drains)
	}
	if lastOK != 0 {
		t.Errorf("lastSuccess = %d, want 0 (failed tick)", lastOK)
	}
}

func TestTailer_ApplyErrorStopsAndReports(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1, 2, 3)}
	store := projection.NewMemOwnerCheckpointStore()
	applyErr := errors.New("apply boom")
	apply := func(_ context.Context, evt projection.ProjectionEvent) error {
		if evt.EventID() == "evt-2" {
			return applyErr
		}
		return nil
	}
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, apply, obs, newTestLocker(t, clk), clk)

	err := tl.pollOnce(context.Background())
	if !errors.Is(err, applyErr) {
		t.Fatalf("pollOnce err = %v, want wraps applyErr", err)
	}
	// evt-1 committed before evt-2 failed.
	off, _ := store.LoadOffset(context.Background(), testCell, testProj)
	if off != 1 {
		t.Errorf("checkpoint = %d, want 1 (only evt-1 committed)", off)
	}
	_, drains, advances, _, _ := obs.snapshot()
	if len(advances) != 1 || advances[0] != AdvanceOK {
		t.Errorf("advances = %v, want [ok] (evt-1 only)", advances)
	}
	if len(drains) != 1 || drains[0] != DrainApplyError {
		t.Errorf("drains = %v, want [apply_error]", drains)
	}
}

func TestTailer_StaleOwnerBenign(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	store := newFakeOwnerStore()
	store.advanceErr = projection.ErrStaleOwner // simulate deposed-leader fence
	src := &fakeSource{events: events(1)}
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, func(context.Context, projection.ProjectionEvent) error { return nil }, obs, newTestLocker(t, clk), clk)

	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("stale owner must be benign, got: %v", err)
	}
	_, drains, advances, _, lastOK := obs.snapshot()
	if len(advances) != 1 || advances[0] != AdvanceStaleOwner {
		t.Errorf("advances = %v, want [stale_owner]", advances)
	}
	for _, d := range drains {
		if d == DrainApplyError {
			t.Errorf("stale owner must not be reported as drain apply_error: %v", drains)
		}
	}
	if lastOK != 1 {
		t.Errorf("lastSuccess = %d, want 1 (benign handoff is a clean tick)", lastOK)
	}
}

func TestTailer_AdvanceNonStaleError(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	store := newFakeOwnerStore()
	store.advanceErr = errors.New("tx commit fault")
	src := &fakeSource{events: events(1)}
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, func(context.Context, projection.ProjectionEvent) error { return nil }, obs, newTestLocker(t, clk), clk)

	if err := tl.pollOnce(context.Background()); err == nil {
		t.Fatal("non-stale advance error must surface as tick error")
	}
	_, drains, advances, _, _ := obs.snapshot()
	if len(advances) != 1 || advances[0] != AdvanceError {
		t.Errorf("advances = %v, want [error]", advances)
	}
	if len(drains) != 1 || drains[0] != DrainApplyError {
		t.Errorf("drains = %v, want [apply_error]", drains)
	}
}

func TestTailer_ConstructorNilGuards(t *testing.T) {
	clk := clockmock.New(time.Unix(0, 0))
	src := &fakeSource{}
	store := projection.NewMemOwnerCheckpointStore()
	apply := func(context.Context, projection.ProjectionEvent) error { return nil }
	locker := newTestLocker(t, clk)
	good := func() (*Tailer, error) {
		return NewTailer(clk, src, src, store, fakeTxRunner{}, apply, locker, testCell, testProj)
	}
	if _, err := good(); err != nil {
		t.Fatalf("baseline NewTailer: %v", err)
	}
	cases := map[string]func() (*Tailer, error){
		"nil replay": func() (*Tailer, error) {
			return NewTailer(clk, nil, src, store, fakeTxRunner{}, apply, locker, testCell, testProj)
		},
		"nil cursor": func() (*Tailer, error) {
			return NewTailer(clk, src, nil, store, fakeTxRunner{}, apply, locker, testCell, testProj)
		},
		"nil store": func() (*Tailer, error) {
			return NewTailer(clk, src, src, nil, fakeTxRunner{}, apply, locker, testCell, testProj)
		},
		"nil tx": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, nil, apply, locker, testCell, testProj)
		},
		"nil apply": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, fakeTxRunner{}, nil, locker, testCell, testProj)
		},
		"nil locker": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, fakeTxRunner{}, apply, nil, testCell, testProj)
		},
		"empty cell": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, fakeTxRunner{}, apply, locker, "", testProj)
		},
		"empty proj": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, fakeTxRunner{}, apply, locker, testCell, "")
		},
	}
	for name, ctor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ctor(); err == nil {
				t.Errorf("%s: want error, got nil", name)
			}
		})
	}
}

func TestTailer_NilClockPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("nil clock must panic (MustHaveClock)")
		}
	}()
	src := &fakeSource{}
	_, _ = NewTailer(nil, src, src, projection.NewMemOwnerCheckpointStore(), fakeTxRunner{},
		func(context.Context, projection.ProjectionEvent) error { return nil },
		newTestLocker(t, clockmock.New(time.Unix(0, 0))), testCell, testProj)
}

func TestTailer_ConfigValidation(t *testing.T) {
	clk := clockmock.New(time.Unix(0, 0))
	src := &fakeSource{}
	mk := func(cfg Config) error {
		_, err := NewTailer(clk, src, src, projection.NewMemOwnerCheckpointStore(), fakeTxRunner{},
			func(context.Context, projection.ProjectionEvent) error { return nil },
			newTestLocker(t, clk), testCell, testProj, WithConfig(cfg))
		return err
	}
	if err := mk(Config{PollInterval: 0, LeaseTTL: time.Second}); err == nil {
		t.Error("zero PollInterval must fail")
	}
	if err := mk(Config{PollInterval: time.Second, LeaseTTL: 0}); err == nil {
		t.Error("zero LeaseTTL must fail")
	}
	if err := mk(Config{PollInterval: time.Second, LeaseTTL: time.Nanosecond}); err == nil {
		t.Error("sub-MinTTL LeaseTTL must fail")
	}
	if err := mk(DefaultConfig()); err != nil {
		t.Errorf("default config must pass: %v", err)
	}
}

func TestTailer_Lifecycle(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1)}
	store := projection.NewMemOwnerCheckpointStore()
	tl := newTestTailer(t, src, store, func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, newTestLocker(t, clk), clk)

	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- tl.Start(ctx) }()
	select {
	case <-tl.Ready():
	case <-time.After(testLifecycleTimeout):
		t.Fatal("Start did not become ready")
	}

	// Double-start while running is a conflict.
	if err := tl.Start(context.Background()); err == nil {
		t.Error("double Start must return conflict")
	}

	if err := tl.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	cancel()
	select {
	case err := <-startErr:
		if err != nil {
			t.Errorf("Start returned: %v", err)
		}
	case <-time.After(testLifecycleTimeout):
		t.Fatal("Start did not return after Stop")
	}

	// Stop before start is a no-op.
	fresh := newTestTailer(t, src, store, func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, newTestLocker(t, clk), clk)
	if err := fresh.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start must be no-op, got: %v", err)
	}
}

func TestClassifyLockSkip(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want LockAcquireResult
	}{
		{"timeout", errcode.New(errcode.KindConflict, errcode.ErrDistlockTimeout, "busy"), LockContended},
		{"ctx canceled", context.Canceled, LockCtxCanceled},
		{"deadline", context.DeadlineExceeded, LockCtxCanceled},
		{"backend", errors.New("redis down"), LockBackendError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyLockSkip(tt.err); got != tt.want {
				t.Errorf("classifyLockSkip(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestTailerLockKeyInjective(t *testing.T) {
	// Length-prefix keeps the key injective over SafeID charset (':' allowed).
	if tailerLockKey("a:b") == tailerLockKey("a") {
		t.Error("tailerLockKey not injective for ':'-containing ids")
	}
	if got := tailerLockKey(testProj); got == "" {
		t.Error("empty key")
	}
}

// TestTailer_LeaderHandoffBoundedDuplicateApply documents the at-least-once
// delivery contract: when a leader handoff occurs, the new leader may re-apply
// an event that the old leader already applied (but whose checkpoint advance was
// rejected with ErrStaleOwner). The duplicate apply is bounded — the checkpoint
// is monotonically non-decreasing and the new leader advances it to the correct
// offset. There is no ErrStaleOwner on the second attempt because the new owner
// token is strictly ahead of the committed checkpoint (cold-claim semantics).
func TestTailer_LeaderHandoffBoundedDuplicateApply(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))

	// Scenario: one event at seq 5. Old leader applied it but was fenced
	// (ErrStaleOwner). New leader reprocesses the same event with a fresh token.

	src := &fakeSource{events: events(5)}
	store := projection.NewMemOwnerCheckpointStore()

	// Count apply calls to verify duplicate-apply is bounded to at most 2.
	applyCount := 0
	apply := func(_ context.Context, evt projection.ProjectionEvent) error {
		applyCount++
		return nil
	}

	// ── First pollOnce (old leader): AdvanceIfOwner returns ErrStaleOwner.
	obs1 := &recordingObserver{}
	fakeOld := &fakeOwnerStore{MemOwnerCheckpointStore: store}
	fakeOld.advanceErr = projection.ErrStaleOwner
	tl1 := newTestTailer(t, src, fakeOld, apply, obs1, newTestLocker(t, clk), clk)
	if err := tl1.pollOnce(context.Background()); err != nil {
		t.Fatalf("old-leader pollOnce: expected benign stale-owner, got: %v", err)
	}
	if applyCount != 1 {
		t.Errorf("old-leader: apply count = %d, want 1", applyCount)
	}
	// Checkpoint must NOT have advanced (stale advance was fenced).
	off, _ := store.LoadOffset(context.Background(), testCell, testProj)
	if off != 0 {
		t.Errorf("checkpoint after stale advance = %d, want 0", off)
	}
	_, _, advances1, _, _ := obs1.snapshot()
	if len(advances1) != 1 || advances1[0] != AdvanceStaleOwner {
		t.Errorf("old-leader advances = %v, want [stale_owner]", advances1)
	}

	// ── Second pollOnce (new leader): fresh token, real store — event re-applied.
	obs2 := &recordingObserver{}
	tl2 := newTestTailer(t, src, store, apply, obs2, newTestLocker(t, clk), clk)
	if err := tl2.pollOnce(context.Background()); err != nil {
		t.Fatalf("new-leader pollOnce: %v", err)
	}
	// Duplicate apply: total apply calls is now 2 (event re-processed once).
	if applyCount != 2 {
		t.Errorf("total apply count = %d, want 2 (at-most-once-ahead duplicate)", applyCount)
	}
	// Checkpoint must now be at seq 5.
	off, _ = store.LoadOffset(context.Background(), testCell, testProj)
	if off != 5 {
		t.Errorf("checkpoint after new-leader advance = %d, want 5", off)
	}
	_, _, advances2, _, _ := obs2.snapshot()
	if len(advances2) != 1 || advances2[0] != AdvanceOK {
		t.Errorf("new-leader advances = %v, want [ok]", advances2)
	}
}

// TestTailer_MidDrainCtxCancel verifies that a ctx cancellation during the
// Replay/apply phase propagates out of pollOnce with the cancellation error.
// The checkpoint must not advance past the last successfully applied event.
func TestTailer_MidDrainCtxCancel(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1, 2, 3)}
	store := projection.NewMemOwnerCheckpointStore()

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context on the second apply call to simulate a mid-drain cancellation.
	applyCount := 0
	apply := func(applyCtx context.Context, _ projection.ProjectionEvent) error {
		applyCount++
		if applyCount == 2 {
			cancel()
			return applyCtx.Err() // propagate cancellation up through Replay
		}
		return nil
	}

	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, apply, obs, newTestLocker(t, clk), clk)

	err := tl.pollOnce(ctx)
	if err == nil {
		t.Fatal("pollOnce: want error from ctx cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("pollOnce err = %v, want context.Canceled", err)
	}

	// Only event 1 was applied successfully before cancellation; checkpoint = 1.
	off, _ := store.LoadOffset(context.Background(), testCell, testProj)
	if off != 1 {
		t.Errorf("checkpoint = %d, want 1 (only evt-1 committed before cancel)", off)
	}
}
