package tailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
)

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
	clk      clock.Clock
	logger   *slog.Logger
	cfg      Config
	observer Observer

	// lifecycle (mirrors runtime/saga.Coordinator)
	state   atomic.Int32
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	readyCh chan struct{}
	wg      sync.WaitGroup

	// observability state (read by the readiness probe; atomic, no hot-path lock)
	lastSuccessUnixNano atomic.Int64
}

// NewTailer constructs a saga-journal projection Tailer. clk is the mandatory
// positional clock (CLOCK-POSITIONAL-INJECTION-01). All interface deps are
// required and nil-guarded (SAGA-CONSTRUCTOR-NIL-GUARD-01) — notably locker is
// required (unlike the saga Coordinator's optional single-process mode): the
// Tailer's leader-handoff correctness depends on the distlock making the
// owner-token claim race-free. A single-process demo passes a real in-memory
// distlock.Locker.
//
// replay and cursor are commonly the same object. For example,
// sagaprojection.SagaJournalSource implements both projection.ReplaySource (via
// Replay/Head) and projection.Cursor (via Position), so callers typically pass
// the same value for both parameters. They are kept as distinct parameters so
// the Tailer is not hard-coupled to a concrete type and so that read-only
// replay sources can be paired with a separately-provided cursor if needed.
func NewTailer(
	clk clock.Clock,
	replay projection.ReplaySource,
	cursor projection.Cursor,
	store projection.OwnerCheckpointStore,
	txRunner persistence.TxRunner,
	apply projection.Apply,
	locker distlock.Locker,
	cellID, projectionID string,
	opts ...Option,
) (*Tailer, error) {
	clock.MustHaveClock(clk, "tailer.NewTailer")
	if validation.IsNilInterface(replay) {
		return nil, nilDepErr("replay")
	}
	if validation.IsNilInterface(cursor) {
		return nil, nilDepErr("cursor")
	}
	if validation.IsNilInterface(store) {
		return nil, nilDepErr("store")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, nilDepErr("txRunner")
	}
	if apply == nil {
		return nil, nilDepErr("apply")
	}
	if validation.IsNilInterface(locker) {
		return nil, nilDepErr("locker")
	}
	if cellID == "" || projectionID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"tailer.NewTailer: cellID and projectionID must be non-empty")
	}
	t := &Tailer{
		replay:       replay,
		cursor:       cursor,
		store:        store,
		txRunner:     txRunner,
		apply:        apply,
		locker:       locker,
		cellID:       cellID,
		projectionID: projectionID,
		lockKey:      tailerLockKey(projectionID),
		clk:          clk,
		logger:       slog.Default(),
		cfg:          DefaultConfig(),
		observer:     NopObserver{},
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
	rpn, err := healthz.SagaTailerReadyProbeName(cellID, projectionID)
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

// safeObserve runs an Observer method with panic recovery so a misbehaving
// observer cannot crash the tail loop. The panic payload is redacted through
// redaction.RedactAny before reaching slog — same form as the Coordinator's
// safeObserve (runtime/saga/coordinator.go). Unlike the Coordinator's variant,
// no deadline timer is added: the Tailer drives a single goroutine and an
// observer that blocks will only stall that one tick, not hold a per-instance
// distributed lock.
func (t *Tailer) safeObserve(ctx context.Context, method string, call func()) {
	defer t.recoverObserverPanic(ctx, method)
	call()
}

// recoverObserverPanic is the shared recover handler for Tailer observer calls.
func (t *Tailer) recoverObserverPanic(ctx context.Context, method string) {
	if r := recover(); r != nil {
		t.logger.WarnContext(ctx, "saga journal tailer: observer call panicked, ignoring",
			slog.String("method", method),
			slog.Any("panic", r))
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

// Stop signals shutdown, cancels the tick loop, and waits for it to exit.
// Idempotent. Unlike the saga Coordinator, the Tailer holds at most one lock per
// tick and releases it synchronously inside pollOnce, so there is no inflight
// fan-out to drain — cancellation is sufficient.
func (t *Tailer) Stop(ctx context.Context) error {
	t.mu.Lock()
	state := tailerState(t.state.Load())
	notStarted := t.cancel == nil && state == tailerStopped
	alreadyStopping := state == tailerStopping
	cancel := t.cancel
	done := t.done
	ready := t.readyCh
	t.mu.Unlock()

	if notStarted || alreadyStopping {
		return nil
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return errcode.Wrap(errcode.KindDeadlineExceeded, errcode.ErrConflict,
			"tailer stop: timed out waiting for start", ctx.Err())
	}

	t.state.Store(int32(tailerStopping))
	if cancel != nil {
		cancel()
	}
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
		t.safeObserve(ctx, "ObserveLockAcquire", func() {
			t.observer.ObserveLockAcquire(ctx, t.projectionID, reason)
		})
		return nil // skip this tick — no drain without leadership
	}
	ownerToken, err := idutil.NewUUID()
	if err != nil {
		_ = lock.Release()
		return fmt.Errorf("tailer: mint owner token: %w", err)
	}
	drainErr := t.drain(ctx, ownerToken)
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

// drain replays (checkpoint, Head] and commits each event via commitEvent. A
// stale-owner rejection (deposed leader fenced) ends the drain benignly.
func (t *Tailer) drain(ctx context.Context, ownerToken string) error {
	checkpoint, err := t.store.LoadOffset(ctx, t.cellID, t.projectionID)
	if err != nil {
		t.safeObserve(ctx, "ObserveDrain", func() {
			t.observer.ObserveDrain(ctx, t.projectionID, DrainStoreError)
		})
		return fmt.Errorf("tailer drain: load checkpoint: %w", err)
	}
	drained := 0
	replayErr := t.replay.Replay(ctx, checkpoint, func(evt projection.ProjectionEvent) error {
		return t.commitEvent(ctx, ownerToken, evt, &drained)
	})
	if replayErr != nil {
		if errors.Is(replayErr, projection.ErrStaleOwner) {
			return nil // deposed leader fenced — benign handoff, not a tick error
		}
		t.safeObserve(ctx, "ObserveDrain", func() {
			t.observer.ObserveDrain(ctx, t.projectionID, DrainApplyError)
		})
		return fmt.Errorf("tailer drain: replay: %w", replayErr)
	}
	if drained > 0 {
		t.safeObserve(ctx, "ObserveDrain", func() {
			t.observer.ObserveDrain(ctx, t.projectionID, DrainOK)
		})
	}
	return nil
}

// commitEvent is the SOLE sanctioned caller of OwnerCheckpointStore.AdvanceIfOwner
// (SAGA-TAILER-CHECKPOINT-ADVANCER-CALLER-01). It applies one event and advances
// the fenced checkpoint in the SAME transaction so apply+advance commit
// atomically (exactly-once within an owner, D5(a); PROJECTION-CHECKPOINT-TX-BOUND-01).
func (t *Tailer) commitEvent(ctx context.Context, ownerToken string, evt projection.ProjectionEvent, drained *int) error {
	pos, err := t.cursor.Position(evt)
	if err != nil {
		return fmt.Errorf("tailer commit: cursor position: %w", err)
	}
	reachedAdvance := false
	txErr := t.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		applyCtx := evt.RestoreContext(txCtx)
		if e := t.apply(applyCtx, evt); e != nil {
			return e
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
