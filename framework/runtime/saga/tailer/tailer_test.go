package tailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/distlock"
	"github.com/ghbvf/gocell/framework/runtime/distlock/locktest"
)

const (
	testCell = "auditcore"
	testProj = "sagastatus"

	// testLockTTL is the distlock TTL used when a test competitor holds the
	// per-projection lock (TEST-TIME-LITERAL-01: durations are package consts).
	testLockTTL = 30 * time.Second
	// testLifecycleTimeout bounds Start/Stop lifecycle waits in tests.
	testLifecycleTimeout = 2 * time.Second
	// testObserverDeadline is a short observer deadline used (with a real clock) to
	// exercise the bounded-observer wrapper (TEST-TIME-LITERAL-01: package const).
	testObserverDeadline = 20 * time.Millisecond
	// testFastPoll is a short poll interval (real clock) so a lifecycle loop ticks
	// into a blocking apply promptly in the retryable-shutdown test
	// (TEST-TIME-LITERAL-01: package const).
	testFastPoll = 5 * time.Millisecond
	// testStopBudget is a short Stop ctx budget used to force a Stop timeout while
	// the loop is deliberately wedged (TEST-TIME-LITERAL-01: package const).
	testStopBudget = 50 * time.Millisecond
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
	// headOverride, when non-nil, makes Head return a fixed bound below the last
	// event seq — simulating a moving tail where events are appended after Head is
	// captured (used to exercise the bounded-drain stop, F2).
	headOverride *int64
}

func (s *fakeSource) Head(context.Context) (int64, error) {
	if s.headErr != nil {
		return 0, s.headErr
	}
	if s.headOverride != nil {
		return *s.headOverride, nil
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

// acquireErrDriver wraps a FakeDriver and injects a SetNX backend I/O error so
// distlock.Acquire returns a non-timeout (backend) error — exercising the
// readiness probe's leader-gate backend dimension (F5). Set err to nil to let
// acquires succeed again.
type acquireErrDriver struct {
	*locktest.FakeDriver
	mu  sync.Mutex
	err error
}

func (d *acquireErrDriver) setErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func (d *acquireErrDriver) SetNX(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	d.mu.Lock()
	e := d.err
	d.mu.Unlock()
	if e != nil {
		return false, e
	}
	return d.FakeDriver.SetNX(ctx, key, token, ttl)
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
	tl, err := newTestTailerWithDeps(clk, src, src, store, apply, locker, WithObserver(obs))
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	return tl
}

func newTestTailerWithDeps(
	clk clock.Clock,
	replay projection.ReplaySource,
	cursor projection.Cursor,
	store projection.OwnerCheckpointStore,
	apply projection.Apply,
	locker distlock.Locker,
	opts ...Option,
) (*Tailer, error) {
	return NewTailer(clk, replay, cursor, store, fakeTxRunner{}, apply, locker, testCell, testProj, opts...)
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
	held, err := locker.Acquire(context.Background(), tailerLockKey(testCell, testProj), testLockTTL)
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

func TestTailer_DrainHeadError(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	headErr := errors.New("saga journal head unavailable")
	src := &fakeSource{events: events(1, 2, 3), headErr: headErr}
	store := projection.NewMemOwnerCheckpointStore()
	applied := 0
	apply := func(context.Context, projection.ProjectionEvent) error { applied++; return nil }
	obs := &recordingObserver{}
	tl := newTestTailer(t, src, store, apply, obs, newTestLocker(t, clk), clk)

	err := tl.pollOnce(context.Background())
	if !errors.Is(err, headErr) {
		t.Fatalf("pollOnce err = %v, want wraps headErr", err)
	}
	// The head-bound fetch fails before replay starts: nothing applied, checkpoint
	// untouched, no success stamp. Distinct from a checkpoint LoadOffset fault
	// (DrainStoreError) — head failure is its own classification.
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (head failed before replay)", applied)
	}
	off, _ := store.LoadOffset(context.Background(), testCell, testProj)
	if off != 0 {
		t.Errorf("checkpoint = %d, want 0 (not advanced)", off)
	}
	_, drains, advances, _, lastOK := obs.snapshot()
	if len(drains) != 1 || drains[0] != DrainHeadError {
		t.Errorf("drains = %v, want [head_error]", drains)
	}
	if len(advances) != 0 {
		t.Errorf("advances = %v, want none (no event reached commit)", advances)
	}
	if lastOK != 0 {
		t.Errorf("lastSuccess = %d, want 0 (failed tick)", lastOK)
	}
}

// TestTailer_NilDepErrRedaction verifies the required-dependency constructor
// error keeps the internal dependency name off the wire (#1884): the dep name
// flows only through the server-only InternalDetails channel, never into the
// public Details that a 4xx response surfaces.
func TestTailer_NilDepErrRedaction(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{}
	store := projection.NewMemOwnerCheckpointStore()
	apply := func(context.Context, projection.ProjectionEvent) error { return nil }
	// nil replay dependency triggers nilDepErr("replay").
	_, err := NewTailer(clk, nil, src, store, fakeTxRunner{}, apply, newTestLocker(t, clk), testCell, testProj)
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("NewTailer err = %v, want *errcode.Error", err)
	}
	// Public wire surface must not carry the internal dependency name. FindAttr
	// searches only e.Details (public); ok==false proves no leak.
	if _, ok := ec.FindAttr("dependency"); ok {
		t.Errorf("dependency name leaked to public Details: %+v", ec.Details)
	}
	// The dep name remains available server-side for diagnosis.
	var internalDep string
	for _, d := range ec.InternalDetails {
		if d.Key() == "dependency" {
			if s, ok := d.Value().(string); ok {
				internalDep = s
			}
		}
	}
	if internalDep != "replay" {
		t.Errorf("internal dependency attr = %q, want %q", internalDep, "replay")
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
	// F7: the apply error carries the event identity + position so a wedged apply
	// is locatable from the tick log without re-deriving it.
	for _, want := range []string{"event_id=evt-2", "position=2", "stream=saga.journal.v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("apply error %q missing diagnostic %q", err.Error(), want)
		}
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
		return newTestTailerWithDeps(clk, src, src, store, apply, locker)
	}
	if _, err := good(); err != nil {
		t.Fatalf("baseline NewTailer: %v", err)
	}
	cases := map[string]func() (*Tailer, error){
		"nil replay": func() (*Tailer, error) {
			return newTestTailerWithDeps(clk, nil, src, store, apply, locker)
		},
		"nil cursor": func() (*Tailer, error) {
			return newTestTailerWithDeps(clk, src, nil, store, apply, locker)
		},
		"nil store": func() (*Tailer, error) {
			return newTestTailerWithDeps(clk, src, src, nil, apply, locker)
		},
		"nil tx": func() (*Tailer, error) {
			return NewTailer(clk, src, src, store, nil, apply, locker, testCell, testProj)
		},
		"nil apply": func() (*Tailer, error) {
			return newTestTailerWithDeps(clk, src, src, store, nil, locker)
		},
		"nil locker": func() (*Tailer, error) {
			return newTestTailerWithDeps(clk, src, src, store, apply, nil)
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
	// Length-prefix keeps the key injective over SafeID charset (':' allowed) on
	// BOTH the cellID and projectionID segments.
	if tailerLockKey("c", "a:b") == tailerLockKey("c", "a") {
		t.Error("tailerLockKey not injective for ':'-containing projectionIDs")
	}
	if tailerLockKey("a:b", "c") == tailerLockKey("a", "b:c") {
		t.Error("tailerLockKey not injective across the cellID/projectionID boundary")
	}
	// F3 regression: two different cells declaring the SAME projectionID (which
	// metadata explicitly permits) MUST get distinct lock keys, otherwise they
	// would contend for one leader lock across cells.
	if tailerLockKey("cellA", testProj) == tailerLockKey("cellB", testProj) {
		t.Error("tailerLockKey collides for same projectionID in different cells")
	}
	if tailerLockKey(testCell, testProj) == "" {
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

// TestTailer_DrainAbortsOnLockLoss verifies the lock-aware drain ctx (F1): when
// the held lock ends mid-drain (here via Orphan — exactly what a Stop handoff
// issues), the drain ctx is canceled, the in-flight apply observes the
// cancellation and aborts, and the checkpoint does not advance. distlock is an
// efficiency lock; aborting here narrows the bounded-duplicate-apply handoff window.
func TestTailer_DrainAbortsOnLockLoss(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	src := &fakeSource{events: events(1, 2, 3)}
	store := projection.NewMemOwnerCheckpointStore()

	var entered sync.Once
	enteredCh := make(chan struct{})
	apply := func(applyCtx context.Context, _ projection.ProjectionEvent) error {
		entered.Do(func() { close(enteredCh) })
		select {
		case <-applyCtx.Done():
			return applyCtx.Err() // drain ctx canceled by lock loss
		case <-time.After(testLifecycleTimeout):
			return errors.New("apply not aborted after lock loss")
		}
	}
	tl := newTestTailer(t, src, store, apply, &recordingObserver{}, newTestLocker(t, clk), clk)

	errCh := make(chan error, 1)
	go func() { errCh <- tl.pollOnce(context.Background()) }()

	<-enteredCh
	lk := tl.inflightLock.Load()
	if lk == nil {
		t.Fatal("inflight lock not registered during drain")
	}
	lk.Orphan() // simulate lock loss / Stop handoff

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("pollOnce err = %v, want wraps context.Canceled (drain aborted on lock loss)", err)
		}
	case <-time.After(testLifecycleTimeout):
		t.Fatal("pollOnce did not return after lock loss")
	}
	if off, _ := store.LoadOffset(context.Background(), testCell, testProj); off != 0 {
		t.Errorf("checkpoint = %d, want 0 (no advance after aborted apply)", off)
	}
}

// TestTailer_StopOrphansInflightLock verifies Stop hands off an in-flight lock by
// Orphan (no Release I/O block) rather than waiting on it (F1). White-box: a real
// held lock is registered as in-flight and the lifecycle is set to a running loop
// whose goroutine has already exited (done closed), so Stop proceeds through the
// orphan branch and returns immediately.
func TestTailer_StopOrphansInflightLock(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	locker := newTestLocker(t, clk)
	tl := newTestTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore(),
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, locker, clk)

	held, err := locker.Acquire(context.Background(), tl.lockKey, testLockTTL)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tl.inflightLock.Store(held)

	done := make(chan struct{})
	close(done)
	ready := make(chan struct{})
	close(ready)
	tl.mu.Lock()
	tl.done = done
	tl.readyCh = ready
	tl.cancel = func() {}
	tl.mu.Unlock()
	tl.state.Store(int32(tailerRunning))

	if err := tl.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cause := held.Cause(); !errors.Is(cause, distlock.ErrLockOrphaned) {
		t.Errorf("in-flight lock cause = %v, want ErrLockOrphaned (Stop must Orphan, not Release)", cause)
	}
}

// TestTailer_StopTimeoutRetryable verifies the retryable-shutdown contract (F1
// check round): when the first Stop's ctx budget is exhausted while the poll loop
// is wedged in a non-cooperative apply, Stop returns a deadline error — and a
// RETRIED Stop must NOT return nil prematurely; it keeps waiting on the same loop-
// exit signal. Only once the loop genuinely exits does a final Stop return nil.
// Pre-fix, the second Stop short-circuited to nil via the alreadyStopping branch,
// falsely reporting a stopped loop that was still draining.
func TestTailer_StopTimeoutRetryable(t *testing.T) {
	src := &fakeSource{events: events(1)}
	store := projection.NewMemOwnerCheckpointStore()

	var entered sync.Once
	enteredCh := make(chan struct{})
	release := make(chan struct{})
	apply := func(context.Context, projection.ProjectionEvent) error {
		entered.Do(func() { close(enteredCh) })
		<-release // non-cooperative: ignore ctx, wedge the loop until released
		return nil
	}

	// Real clock + short poll interval so the loop ticks into apply promptly.
	realClock := clock.Real()
	tl, err := NewTailer(realClock, src, src, store, fakeTxRunner{}, apply, newTestLocker(t, realClock), testCell, testProj,
		WithConfig(Config{PollInterval: testFastPoll, LeaseTTL: testLockTTL}))
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- tl.Start(ctx) }()

	<-enteredCh // loop is wedged inside the non-cooperative apply

	// First Stop times out: the loop ignores ctx, so its goroutine never exits
	// within the budget and done is not closed.
	sctx1, scancel1 := context.WithTimeout(context.Background(), testStopBudget)
	defer scancel1()
	stopErr := tl.Stop(sctx1)
	if stopErr == nil {
		t.Fatal("first Stop must time out while the loop is wedged, got nil")
	}
	// The Stop-budget timeout carries the saga-specific timeout code (#1950),
	// not generic ErrConflict.
	var ecErr *errcode.Error
	if !errors.As(stopErr, &ecErr) {
		t.Fatalf("Stop timeout: expected *errcode.Error, got %T: %v", stopErr, stopErr)
	}
	if ecErr.Kind != errcode.KindDeadlineExceeded || ecErr.Code != errcode.ErrSagaStopTimeout {
		t.Errorf("Stop timeout: kind=%v code=%v, want KindDeadlineExceeded / ErrSagaStopTimeout", ecErr.Kind, ecErr.Code)
	}

	// Retry Stop: state is tailerStopping. It must keep waiting on the same done
	// (the loop is still wedged) and time out again — NOT return nil. This is the
	// regression the fix closes.
	sctx2, scancel2 := context.WithTimeout(context.Background(), testStopBudget)
	defer scancel2()
	if err := tl.Stop(sctx2); err == nil {
		t.Fatal("retry Stop returned nil while the loop was still draining (must keep waiting on done)")
	}

	// Release the wedge → the loop drains the tick, observes the canceled ctx, and
	// exits, closing done.
	close(release)

	// A final Stop with ample budget now observes genuine loop exit and returns nil.
	sctx3, scancel3 := context.WithTimeout(context.Background(), testLifecycleTimeout)
	defer scancel3()
	if err := tl.Stop(sctx3); err != nil {
		t.Errorf("final Stop after loop exit = %v, want nil", err)
	}
	select {
	case <-startErr:
	case <-time.After(testLifecycleTimeout):
		t.Fatal("Start did not return after the loop exited")
	}
}

// TestTailer_DrainBoundedByHead verifies a single drain is bounded by the Head
// captured at tick start (F2): with events 1..5 but Head pinned to 3, only events
// ≤ 3 are drained this tick; 4 and 5 are left for the next tick instead of being
// chased as a moving tail.
func TestTailer_DrainBoundedByHead(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	headBound := int64(3)
	src := &fakeSource{events: events(1, 2, 3, 4, 5), headOverride: &headBound}
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
		t.Errorf("applied = %v, want %v (bounded at Head=3)", applied, want)
	}
	if off, _ := store.LoadOffset(context.Background(), testCell, testProj); off != 3 {
		t.Errorf("checkpoint = %d, want 3 (bounded at Head)", off)
	}
	_, drains, advances, _, _ := obs.snapshot()
	if len(advances) != 3 {
		t.Errorf("advances = %v, want 3 (bounded at Head)", advances)
	}
	if len(drains) != 1 || drains[0] != DrainOK {
		t.Errorf("drains = %v, want [ok]", drains)
	}
}

// TestTailer_BlockingObserverBounded verifies a blocking observer cannot pin the
// held-lock drain (F3): safeObserve abandons the call after observerCallDeadline
// and returns. Uses a real clock + short deadline to avoid fake-timer
// registration races; the abandoned observer goroutine is released at test end.
func TestTailer_BlockingObserverBounded(t *testing.T) {
	tl := newTestTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore(),
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, newTestLocker(t, clockmock.New(time.Unix(0, 0))), clock.Real())
	tl.observerCallDeadline = testObserverDeadline

	release := make(chan struct{})
	defer close(release) // let the abandoned observer goroutine exit at test end
	blocked := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		tl.safeObserve(context.Background(), "Blocking", func() {
			close(blocked)
			<-release
		})
		close(returned)
	}()

	<-blocked
	select {
	case <-returned:
	case <-time.After(testLifecycleTimeout):
		t.Fatal("safeObserve did not return after observerCallDeadline despite blocked observer")
	}
}

// TestTailer_ProbeFailsOnLockBackendError verifies the readiness probe reflects
// leader-gate distlock backend health (F5): a backend acquire fault makes a
// running tailer not-ready (contended would not), and a subsequent successful
// acquire clears it.
func TestTailer_ProbeFailsOnLockBackendError(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	drv := &acquireErrDriver{FakeDriver: locktest.NewFakeDriver()}
	locker, err := distlock.New(drv, clk)
	if err != nil {
		t.Fatalf("distlock.New: %v", err)
	}
	tl := newTestTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore(),
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, locker, clk)
	tl.state.Store(int32(tailerRunning)) // probe only reports when running

	// Healthy baseline.
	if err := tl.checkReady(context.Background()); err != nil {
		t.Fatalf("baseline checkReady = %v, want nil", err)
	}

	// Inject a backend acquire fault → tick skips, probe must turn unhealthy.
	drv.setErr(errors.New("redis down"))
	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce with backend error must skip cleanly, got: %v", err)
	}
	if tl.lockBackendErrUnixNano.Load() == 0 {
		t.Fatal("backend error not recorded")
	}
	if err := tl.checkReady(context.Background()); err == nil {
		t.Fatal("checkReady = nil, want unhealthy after leader-gate backend fault")
	}

	// Backend recovers → next successful acquire clears the fault → probe healthy.
	drv.setErr(nil)
	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce after recovery: %v", err)
	}
	if err := tl.checkReady(context.Background()); err != nil {
		t.Errorf("checkReady = %v, want nil after backend recovery", err)
	}
}

// TestTailer_ProbeStaysHealthyOnContention verifies a contended acquire (another
// replica leads) is NOT treated as a backend fault — the probe stays healthy (F5).
func TestTailer_ProbeStaysHealthyOnContention(t *testing.T) {
	clk := clockmock.New(time.Unix(1000, 0))
	locker := newTestLocker(t, clk)
	tl := newTestTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore(),
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, locker, clk)
	tl.state.Store(int32(tailerRunning))

	// A competitor holds the lock → acquire is contended, not a backend fault.
	held, err := locker.Acquire(context.Background(), tl.lockKey, testLockTTL)
	if err != nil {
		t.Fatalf("competitor acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	if err := tl.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if tl.lockBackendErrUnixNano.Load() != 0 {
		t.Error("contended acquire must not record a backend error")
	}
	if err := tl.checkReady(context.Background()); err != nil {
		t.Errorf("checkReady = %v, want nil (contention is normal multi-replica)", err)
	}
}

// TestTailer_ObserverPanicRedacted verifies a panicking observer is recovered and
// its payload is redacted before reaching slog (F8): a secret in the panic value
// must not appear in the log output.
func TestTailer_ObserverPanicRedacted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	tl := newTestTailer(t, &fakeSource{}, projection.NewMemOwnerCheckpointStore(),
		func(context.Context, projection.ProjectionEvent) error { return nil },
		&recordingObserver{}, newTestLocker(t, clockmock.New(time.Unix(0, 0))),
		clockmock.New(time.Unix(0, 0)))
	tl.logger = logger

	// Panic with a string value so RedactAny's string branch yields a redacted
	// string that any slog handler renders visibly (an error value would marshal
	// to {} under a plain JSON handler — secret hidden but no visible marker).
	tl.safeObserve(context.Background(), "Panicking", func() {
		panic("token=supersecret123")
	})

	out := buf.String()
	if strings.Contains(out, "supersecret123") {
		t.Errorf("panic log leaked secret: %s", out)
	}
	if !strings.Contains(out, "REDACTED") {
		t.Errorf("panic log not redacted (no REDACTED marker): %s", out)
	}
}
