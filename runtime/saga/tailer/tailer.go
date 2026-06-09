package tailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// errStopAtHead is an internal sentinel returned by the drain replay callback to
// stop replay once the fixed Head bound captured at the start of the tick is
// reached. It bounds a single drain to (checkpoint, Head] so the tailer cannot
// chase a moving tail under continuous writes (the ReplaySource pages forward
// until a short page and would otherwise keep following newly-appended events
// within one tick). It never escapes drain.
var errStopAtHead = errors.New("tailer: reached captured head bound")

// compile-time: Tailer is a lifecycle.ManagedResource (bootstrap wires it via
// WithManagedResource in PR-05) and its own worker.Worker (Start/Stop = the poll
// loop). HEALTH-AGG-01 requires any runtime type exposing Probes() to implement
// ManagedResource.
var (
	_ lifecycle.ManagedResource = (*Tailer)(nil)
	_ worker.Worker             = (*Tailer)(nil)
)

// tailerState is the Tailer lifecycle state machine (mirrors runtime/saga
// coordState). The zero value is stopped.
type tailerState int32

const (
	tailerStopped  tailerState = iota // zero value = stopped
	tailerStarting                    // Start() entered, loop launching
	tailerRunning                     // tick loop active
	tailerStopping                    // Stop() called, waiting for the loop
)

// Tailer is a catch-up tailing live driver for a saga-journal projection
// (EPIC #1609 PR-04). It is an INDEPENDENT component (ADR D4 "Option B") — it
// does NOT piggyback the saga Coordinator's per-instance tick/leader-gate (the
// Coordinator's distlock is per-saga-instance, not a reusable global/per-
// projection leader). It shares the same distlock.Locker instance but acquires a
// distinct per-projection key.
//
// Per tick (PollInterval): acquire the per-projection distlock (leader gate;
// no-lock → skip, fail-closed, so two processes never drain concurrently under
// normal operation), mint a fresh owner token, drain (checkpoint, Head] by
// replaying each event through Apply and advancing the checkpoint in the SAME
// transaction (exactly-once within an owner, D5(a)), then release the lock.
//
// # Leader-handoff fencing (D5(b), mem this PR; PG owner-column → PR-PG)
//
// distlock is an efficiency lock, not the correctness boundary. During the
// lock-expiry/handoff window two processes may briefly both believe they lead.
// The OwnerCheckpointStore.AdvanceIfOwner CAS (semantics B) fences checkpoint
// REGRESSION — a lagging deposed leader's advance is rejected with
// ErrStaleOwner — but a deposed-yet-ahead leader can still re-claim, so a tight
// handoff race may cause BOUNDED duplicate apply. Projection Apply MUST therefore
// be idempotent; this is at-least-once delivery with monotonic-checkpoint
// fencing, exactly-once only within a single owner. Full monotonic fencing (PG
// owner-column fence token) is deferred to PR-PG per ADR D5(b) §5 (the
// leader-handoff threat row stays ⚠️ until then).
type Tailer struct {
	// required deps (nil-guarded in NewTailer)
	replay   projection.ReplaySource         // Head + Replay (PR-03 SagaJournalSource)
	cursor   projection.Cursor               // Position(evt) → GlobalSeq
	store    projection.OwnerCheckpointStore // fenced checkpoint (LoadOffset + AdvanceIfOwner)
	txRunner persistence.TxRunner            // apply + advance committed in one tx (D5a)
	apply    projection.Apply                // func(ctx, ProjectionEvent) error
	locker   distlock.Locker                 // shared instance, per-projection key

	// identity
	cellID         string
	projectionID   string
	lockKey        string
	readyProbeName healthz.ProbeName

	// optional (defaulted)
	clk                  clock.Clock
	logger               *slog.Logger
	cfg                  Config
	observer             Observer
	observerCallDeadline time.Duration // bounds each Observer call (F3); default defaultObserverCallDeadline

	// lifecycle (mirrors runtime/saga.Coordinator)
	state   atomic.Int32
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{}
	wg      sync.WaitGroup

	// inflightLock holds the per-projection distlock currently held during a drain
	// (nil when idle). Stop reads it to Orphan a held lock for prompt leader
	// handoff without blocking on Release I/O (F1).
	inflightLock atomic.Pointer[distlock.Lock]

	// observability state (read by the readiness probe; atomic, no hot-path lock)
	lastSuccessUnixNano atomic.Int64
	// lockBackendErrUnixNano is the unix-nano time of the most recent distlock
	// acquire that failed with a backend I/O fault (0 = leader-gate backend last
	// seen reachable). The readiness probe fails while non-zero so a tailer that
	// cannot reach its leader-gate backend does not report ready (F5). Contended /
	// ctx-canceled acquires clear it (the backend responded).
	lockBackendErrUnixNano atomic.Int64
}

// NewTailer constructs a saga-journal projection Tailer. clk is the mandatory
// positional clock (CLOCK-POSITIONAL-INJECTION-01). All interface deps are
// required and nil-guarded (SAGA-CONSTRUCTOR-NIL-GUARD-01) — notably locker is
// required (unlike the saga Coordinator's optional single-process mode): the
// Tailer's leader-handoff correctness depends on the distlock making the
// owner-token claim race-free. A single-process demo passes a real in-memory
// distlock.Locker.
//
// TailerDeps bundles the Tailer's required collaborators. Replay and Cursor are
// commonly the same object. For example, sagaprojection.SagaJournalSource
// implements both projection.ReplaySource (via Replay/Head) and projection.Cursor
// (via Position), so callers typically pass the same value for both fields. They
// remain distinct so the Tailer is not hard-coupled to a concrete type and so
// read-only replay sources can be paired with a separately-provided cursor.
type TailerDeps struct {
	Replay       projection.ReplaySource
	Cursor       projection.Cursor
	Store        projection.OwnerCheckpointStore
	TxRunner     persistence.TxRunner
	Apply        projection.Apply
	Locker       distlock.Locker
	CellID       string
	ProjectionID string
}

func NewTailer(clk clock.Clock, deps TailerDeps, opts ...Option) (*Tailer, error) {
	clock.MustHaveClock(clk, "tailer.NewTailer")
	if validation.IsNilInterface(deps.Replay) {
		return nil, nilDepErr("replay")
	}
	if validation.IsNilInterface(deps.Cursor) {
		return nil, nilDepErr("cursor")
	}
	if validation.IsNilInterface(deps.Store) {
		return nil, nilDepErr("store")
	}
	if validation.IsNilInterface(deps.TxRunner) {
		return nil, nilDepErr("txRunner")
	}
	if deps.Apply == nil {
		return nil, nilDepErr("apply")
	}
	if validation.IsNilInterface(deps.Locker) {
		return nil, nilDepErr("locker")
	}
	if deps.CellID == "" || deps.ProjectionID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"tailer.NewTailer: cellID and projectionID must be non-empty")
	}
	t := &Tailer{
		replay:               deps.Replay,
		cursor:               deps.Cursor,
		store:                deps.Store,
		txRunner:             deps.TxRunner,
		apply:                deps.Apply,
		locker:               deps.Locker,
		cellID:               deps.CellID,
		projectionID:         deps.ProjectionID,
		lockKey:              tailerLockKey(deps.ProjectionID),
		clk:                  clk,
		logger:               slog.Default(),
		cfg:                  DefaultConfig(),
		observer:             NopObserver{},
		observerCallDeadline: defaultObserverCallDeadline,
	}
	for _, o := range opts {
		o(t)
	}
	if err := t.cfg.Validate(); err != nil {
		return nil, err
	}
	if t.cfg.LeaseTTL < distlock.MinTTL {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"tailer.NewTailer: Config.LeaseTTL must be ≥ distlock.MinTTL (it doubles as the distlock TTL)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("leaseTTL=%s minTTL=%s", t.cfg.LeaseTTL, distlock.MinTTL))))
	}
	rpn, err := healthz.SagaTailerReadyProbeName(deps.CellID, deps.ProjectionID)
	if err != nil {
		return nil, fmt.Errorf("tailer.NewTailer: readiness probe name: %w", err)
	}
	t.readyProbeName = rpn
	t.readyCh = make(chan struct{})
	return t, nil
}

// Worker implements lifecycle.ManagedResource. The Tailer is its own
// worker.Worker — Start runs the poll loop, Stop drains it.
func (t *Tailer) Worker() worker.Worker { return t }

// Close implements lifecycle.ManagedResource. It delegates to Stop(ctx)
// (idempotent: Stop returns nil when never started). Bootstrap calls Close in
// LIFO order during phase10 shutdown.
func (t *Tailer) Close(ctx context.Context) error { return t.Stop(ctx) }

// safeObserve runs an Observer method with two layers of fail-closed protection,
// mirroring runtime/saga.(*Coordinator).safeObserve:
//
//  1. Panic recovery: a panicking observer logs Warn (redacted payload) and the
//     tail loop continues.
//
//  2. Bounded wait: the call runs on a fresh goroutine and the caller waits at
//     most t.observerCallDeadline before logging Warn and returning. Drain-path
//     observer calls (ObserveDrain / ObserveCheckpointAdvance) execute while the
//     per-projection distlock is HELD — pollOnce releases the lock only after
//     drain returns — so a blocking observer would otherwise pin leadership and
//     stall the drain. The bound caps that to one deadline window (F3).
//
// A leaked observer goroutine may run indefinitely (Go cannot kill a goroutine);
// this is bounded by Observer impl quality — the Observer contract requires
// non-blocking methods.
func (t *Tailer) safeObserve(ctx context.Context, method string, call func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer t.recoverObserverPanic(ctx, method)
		call()
	}()
	timer := t.clk.NewTimerAt(t.clk.Now().Add(t.observerCallDeadline))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C():
		t.logger.WarnContext(ctx, "saga journal tailer: observer call exceeded deadline; continuing",
			slog.String("method", method),
			slog.Duration("deadline", t.observerCallDeadline))
	}
}

// recoverObserverPanic is the shared recover handler for Tailer observer calls.
// The panic payload is redacted through redaction.RedactAny before reaching slog
// so a panic value carrying user data does not leak into operator logs — same
// form as runtime/saga.(*Coordinator).recoverObserverPanic.
func (t *Tailer) recoverObserverPanic(ctx context.Context, method string) {
	if r := recover(); r != nil {
		t.logger.WarnContext(ctx, "saga journal tailer: observer call panicked, ignoring",
			slog.String("method", method),
			slog.Any("panic", redaction.RedactAny(r)))
	}
}

func nilDepErr(dep string) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"tailer.NewTailer: required dependency is nil",
		errcode.WithDetails(errcode.PublicString("dependency", dep)),
		errcode.WithInternal(errcode.InternalAttr("dependency", dep)))
}

// tailerLockKey builds the per-projection distlock key. projectionID may contain
// ':' (SafeID charset), so length-prefix for injectivity, mirroring
// runtime/saga.leaderElectLockKey. The "saga-journal-tailer:" namespace keeps
// the key disjoint from the saga Coordinator's per-instance "saga:" keys even
// though both share the same distlock.Locker instance.
func tailerLockKey(projectionID string) string {
	return fmt.Sprintf("saga-journal-tailer:%d:%s", len(projectionID), projectionID)
}

// Start launches the tick loop and blocks until ctx is canceled or Stop is
// called. Mirrors runtime/saga.Coordinator.Start.
func (t *Tailer) Start(ctx context.Context) error {
	if !t.state.CompareAndSwap(int32(tailerStopped), int32(tailerStarting)) {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"tailer: already started or starting")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	t.mu.Lock()
	t.cancel = cancel
	t.done = done
	t.wg.Add(1)
	t.mu.Unlock()

	t.state.Store(int32(tailerRunning))
	close(t.readyCh)
	t.logger.InfoContext(ctx, "saga journal tailer: started",
		slog.String("cell", t.cellID),
		slog.String("projection", t.projectionID),
		slog.Duration("lease_ttl", t.cfg.LeaseTTL))

	// Seed the last-success-timestamp series for this {cell,projection} at start
	// (baseline = start time) so a never-succeeding projection is detectable
	// per-label: the stalled-tailer alert's first arm fires once start age exceeds
	// its threshold. Without the seed the series only appears on the first
	// successful tick, so a cold {cell,projection} would have no series and could
	// only be caught by a global absent() that cannot pin a single projection (F6).
	t.safeObserve(ctx, "ObserveLastSuccess", func() {
		t.observer.ObserveLastSuccess(ctx, t.projectionID, t.clk.Now())
	})

	defer func() {
		t.wg.Wait()
		t.mu.Lock()
		t.cancel = nil
		t.done = nil
		t.readyCh = make(chan struct{}) // fresh; next Start closes it
		t.state.Store(int32(tailerStopped))
		close(done)
		t.mu.Unlock()
	}()

	go func() { defer t.wg.Done(); t.tickLoop(ctx) }()

	<-ctx.Done()
	return nil
}

// Stop signals shutdown, cancels the tick loop, and waits for it to exit within
// ctx's budget. Idempotent AND retryable: if ctx is exhausted before the loop
// drains, Stop returns a deadline error but leaves shutdown in progress (state
// stays tailerStopping; the cancel + in-flight lock Orphan have already been
// issued). A subsequent Stop/Close then re-enters the alreadyStopping branch and
// keeps waiting on the SAME loop-exit signal rather than reporting a premature
// success — mirroring kernel/reconcile.(*Loop).Stop's retryable-shutdown
// semantics. Returning nil from a timed-out shutdown would let callers (e.g.
// bootstrap LIFO Close) believe the loop has stopped while it is still draining.
//
// Unlike the saga Coordinator, the Tailer holds at most one lock per tick and
// releases it synchronously inside pollOnce, so there is no inflight fan-out to
// drain — cancellation (plus the in-flight lock Orphan) is sufficient.
func (t *Tailer) Stop(ctx context.Context) error {
	t.mu.Lock()
	state := tailerState(t.state.Load())
	notStarted := t.cancel == nil && state == tailerStopped
	alreadyStopping := state == tailerStopping
	done := t.done
	ready := t.readyCh
	t.mu.Unlock()

	if notStarted {
		return nil
	}
	if alreadyStopping {
		// A prior (possibly timed-out) or concurrent Stop already issued cancel +
		// lock Orphan; the loop is draining. Wait on the SAME loop-exit signal
		// instead of returning nil prematurely, so a retried Stop keeps retryable
		// shutdown semantics (F1 check round). done is non-nil here: under t.mu,
		// state==tailerStopping implies Start's teardown — which nils done and stores
		// tailerStopped in the same mu section — has not run.
		return t.awaitLoopExit(ctx, done)
	}

	// Wait until Start has populated cancel/done and reached running.
	select {
	case <-ready:
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errcode.ErrConflict,
			"tailer stop: timed out waiting for start", ctx.Err())
	}

	t.state.Store(int32(tailerStopping))

	// Re-read cancel/done now that ready is closed (Start is past its mu section);
	// capturing them at the top would race a concurrent Start that had not yet
	// stored them. Mirrors runtime/saga.(*Coordinator).Stop's point-of-use re-read.
	t.mu.Lock()
	cancel := t.cancel
	done = t.done
	t.mu.Unlock()

	// Orphan any in-flight lock BEFORE canceling so a graceful Stop hands off
	// leadership promptly: Orphan stops renewal without a Release RPC (never blocks
	// on backend reachability) and closes the lock's Done() channel, which the
	// drain's lock-aware ctx observes to abort apply/advance immediately. pollOnce's
	// subsequent lock.Release() then becomes a no-op (Orphan won the shared
	// sync.Once). The deposed leader stops renewing; the competitor takes over on
	// the lease TTL (F1).
	if lk := t.inflightLock.Load(); lk != nil {
		lk.Orphan()
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil // Start's teardown already ran between the ready wait and here.
	}
	return t.awaitLoopExit(ctx, done)
}

// awaitLoopExit blocks until the poll-loop goroutine has fully exited (Start's
// deferred teardown closes done) or ctx's budget is exhausted. On timeout it
// returns a deadline error WITHOUT clearing lifecycle state, so a subsequent
// Stop/Close re-selects on the same done — the retryable-shutdown contract.
func (t *Tailer) awaitLoopExit(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errcode.ErrConflict,
			"tailer stop: timed out waiting for loop exit", ctx.Err())
	}
}

// Ready returns a channel closed once Start transitions to running.
func (t *Tailer) Ready() <-chan struct{} {
	t.mu.Lock()
	ch := t.readyCh
	t.mu.Unlock()
	return ch
}

func (t *Tailer) tickLoop(ctx context.Context) {
	ticker := t.clk.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			if err := t.pollOnce(ctx); err != nil {
				t.logger.WarnContext(ctx, "saga journal tailer: tick failed",
					slog.String("cell", t.cellID),
					slog.String("projection", t.projectionID),
					slog.Any("error", err))
			}
		}
	}
}

// pollOnce is the leader-gated drain cycle. The distlock acquire is the leader
// gate (mirrors SAGA-DRIVE-BEHIND-LEADER-GATE-01 semantics): on any acquire
// failure the tick is a no-op (no drain without the lock, fail-closed).
//
// NOTE: this method is deliberately named pollOnce, NOT tickOnce. The
// SAGA-DRIVE-BEHIND-LEADER-GATE-01 archtest locks the Coordinator's driveOne
// caller to a function named exactly "tickOnce". Naming this method tickOnce
// would trigger that archtest sentinel and cause a false-positive CI failure.
// pollOnce is the Tailer-local analog and is intentionally distinct.
func (t *Tailer) pollOnce(ctx context.Context) error {
	if tailerState(t.state.Load()) == tailerStopping {
		return nil
	}
	lock, err := t.locker.Acquire(ctx, t.lockKey, t.cfg.LeaseTTL)
	if err != nil {
		reason := classifyLockSkip(err)
		t.recordLockAcquireOutcome(reason)
		t.safeObserve(ctx, "ObserveLockAcquire", func() {
			t.observer.ObserveLockAcquire(ctx, t.projectionID, reason)
		})
		return nil // skip this tick — no drain without leadership
	}
	t.lockBackendErrUnixNano.Store(0) // acquire succeeded → leader-gate backend reachable (F5)
	t.inflightLock.Store(lock)
	defer t.inflightLock.Store(nil)

	ownerToken, err := idutil.NewUUID()
	if err != nil {
		_ = lock.Release()
		return fmt.Errorf("tailer: mint owner token: %w", err)
	}

	// Derive a lock-aware drain ctx: abort the drain (stop applying/advancing) the
	// moment the held lock ends — renewal failure (ErrLockLost) or a Stop-issued
	// Orphan — so a deposed leader does not keep mutating the projection during a
	// handoff window. distlock is an efficiency lock, not the correctness boundary
	// (the AdvanceIfOwner CAS fences checkpoint regression); aborting here narrows
	// the bounded-duplicate-apply window per ADR D5(b) (F1).
	drainCtx, cancelDrain := context.WithCancel(ctx)
	defer cancelDrain()
	go t.watchLock(drainCtx, lock, cancelDrain)

	drainErr := t.drain(drainCtx, ownerToken)
	if rerr := lock.Release(); rerr != nil {
		t.logger.WarnContext(ctx, "saga journal tailer: distlock release failed",
			slog.String("lock_key", t.lockKey), slog.Any("error", rerr))
	}
	if drainErr != nil {
		return drainErr
	}
	t.recordTickSuccess(ctx)
	return nil
}

// watchLock cancels the drain ctx when the held lock ends (renewal failure or a
// Stop-issued Orphan). It exits when the drain finishes (drainCtx canceled by
// pollOnce's deferred cancel), so it never outlives a tick.
func (t *Tailer) watchLock(drainCtx context.Context, lock *distlock.Lock, cancelDrain context.CancelFunc) {
	select {
	case <-lock.Done():
		cancelDrain()
	case <-drainCtx.Done():
	}
}

// recordLockAcquireOutcome tracks leader-gate backend health for the readiness
// probe (F5). A backend I/O fault stamps the failure time; contended (another
// holder) and ctx-canceled (shutdown) acquires prove the backend responded and
// clear the stamp. A successful acquire also clears it (see pollOnce).
func (t *Tailer) recordLockAcquireOutcome(reason LockAcquireResult) {
	if reason == LockBackendError {
		t.lockBackendErrUnixNano.Store(t.clk.Now().UnixNano())
		return
	}
	t.lockBackendErrUnixNano.Store(0)
}

// drain replays (checkpoint, Head] and commits each event via commitEvent, where
// Head is a FIXED upper bound captured once at the start of the drain. Bounding
// the range stops a single tick from chasing a moving tail under continuous
// writes: the ReplaySource pages forward until a short page, so without a fixed
// bound a high-write-rate journal could keep an open-ended drain following
// newly-appended events indefinitely within one tick. Events appended after Head
// are left for the next tick (F2). A stale-owner rejection (deposed leader
// fenced) ends the drain benignly.
func (t *Tailer) drain(ctx context.Context, ownerToken string) error {
	head, err := t.replay.Head(ctx)
	if err != nil {
		t.observeDrain(ctx, DrainStoreError)
		return fmt.Errorf("tailer drain: head: %w", err)
	}
	checkpoint, err := t.store.LoadOffset(ctx, t.cellID, t.projectionID)
	if err != nil {
		t.observeDrain(ctx, DrainStoreError)
		return fmt.Errorf("tailer drain: load checkpoint: %w", err)
	}
	drained := 0
	replayErr := t.replay.Replay(ctx, checkpoint, func(evt projection.ProjectionEvent) error {
		pos, perr := t.cursor.Position(evt)
		if perr != nil {
			return fmt.Errorf("tailer drain: cursor position: %w", perr)
		}
		if pos > head {
			return errStopAtHead // bounded drain reached the captured Head — stop this tick
		}
		return t.commitEvent(ctx, ownerToken, evt, pos, &drained)
	})
	switch {
	case replayErr == nil, errors.Is(replayErr, errStopAtHead):
		// clean drain or benign bounded stop — fall through to the success path.
	case errors.Is(replayErr, projection.ErrStaleOwner):
		return nil // deposed leader fenced — benign handoff, not a tick error
	default:
		t.observeDrain(ctx, DrainApplyError)
		return fmt.Errorf("tailer drain: replay: %w", replayErr)
	}
	if drained > 0 {
		t.observeDrain(ctx, DrainOK)
	}
	return nil
}

// observeDrain reports a drain outcome through the bounded observer funnel.
func (t *Tailer) observeDrain(ctx context.Context, result DrainResult) {
	t.safeObserve(ctx, "ObserveDrain", func() {
		t.observer.ObserveDrain(ctx, t.projectionID, result)
	})
}

// commitEvent is the SOLE sanctioned caller of OwnerCheckpointStore.AdvanceIfOwner
// (SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01). It applies one event at the
// already-resolved cursor position pos and advances the fenced checkpoint in the
// SAME transaction so apply+advance commit atomically (exactly-once within an
// owner, D5(a); PROJECTION-CHECKPOINT-TX-BOUND-01).
func (t *Tailer) commitEvent(ctx context.Context, ownerToken string, evt projection.ProjectionEvent, pos int64, drained *int) error {
	reachedAdvance := false
	txErr := t.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		applyCtx := evt.RestoreContext(txCtx)
		if e := t.apply(applyCtx, evt); e != nil {
			// Carry the event identity + position so a wedged apply is locatable
			// from the tick-error log without re-deriving it (F7).
			return fmt.Errorf("tailer commit: apply event_id=%s stream=%s position=%d: %w",
				evt.EventID(), evt.Stream(), pos, e)
		}
		reachedAdvance = true // the next call is the AdvanceIfOwner — failures past here are advance faults
		return t.store.AdvanceIfOwner(txCtx, t.cellID, t.projectionID, ownerToken, pos)
	})
	if txErr != nil {
		if reachedAdvance {
			t.observeAdvanceFailure(ctx, txErr)
		}
		return txErr
	}
	*drained++
	t.safeObserve(ctx, "ObserveCheckpointAdvance", func() {
		t.observer.ObserveCheckpointAdvance(ctx, t.projectionID, AdvanceOK)
	})
	return nil
}

// observeAdvanceFailure classifies a checkpoint-advance failure into the metric
// label (stale_owner is a benign handoff; anything else is a fault).
func (t *Tailer) observeAdvanceFailure(ctx context.Context, err error) {
	result := AdvanceError
	if errors.Is(err, projection.ErrStaleOwner) {
		result = AdvanceStaleOwner
	}
	t.safeObserve(ctx, "ObserveCheckpointAdvance", func() {
		t.observer.ObserveCheckpointAdvance(ctx, t.projectionID, result)
	})
}

// recordTickSuccess stamps the last-success timestamp and reports the residual
// pending backlog after a clean tick.
func (t *Tailer) recordTickSuccess(ctx context.Context) {
	now := t.clk.Now()
	t.lastSuccessUnixNano.Store(now.UnixNano())
	t.safeObserve(ctx, "ObserveLastSuccess", func() {
		t.observer.ObserveLastSuccess(ctx, t.projectionID, now)
	})
	if pending, err := t.computePending(ctx); err == nil {
		t.safeObserve(ctx, "ObserveLag", func() {
			t.observer.ObserveLag(ctx, t.projectionID, pending)
		})
	}
}

// classifyLockSkip maps a distlock acquire error to its typed skip reason — the
// single source so the metric label never diverges. Contention and ctx
// cancellation are expected; anything else is a distlock backend I/O fault.
func classifyLockSkip(err error) LockAcquireResult {
	var ec *errcode.Error
	switch {
	case errors.As(err, &ec) && ec.Code == errcode.ErrDistlockTimeout:
		return LockContended
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return LockCtxCanceled
	default:
		return LockBackendError
	}
}
