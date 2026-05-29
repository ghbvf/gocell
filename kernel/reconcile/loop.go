package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
)

const (
	// defaultReconcileInterval is the requeue delay used when a Reconciler
	// returns Result{} (RequeueAfter == 0) or a transient error.
	defaultReconcileInterval = 30 * time.Second
	// defaultMaxConcurrentReconciles is the worker count when unset.
	defaultMaxConcurrentReconciles = 1
	defaultLoopName                = "reconcile.loop"
	// reconcilerIDSentinel labels metrics when ReconcilerID is unset. It reuses
	// the observability.md "_runtime" framework/unknown-owner sentinel (same as
	// the HTTP metrics cell label and the transplant source's cellID default) so
	// owner-dimension filters stay consistent across metrics — e.g. a dashboard
	// can exclude unowned series with reconciler!="_runtime" exactly as it does
	// cell!="_runtime".
	reconcilerIDSentinel = "_runtime"
	// startProbeTimeout bounds how long Start waits for the worker pool to
	// confirm it is running before returning (fast-return OnStart contract).
	startProbeTimeout = 50 * time.Millisecond
	// queueBuffer sizes the internal work queue. Source and requeue both feed
	// it; backpressure (a full buffer) blocks the producer, never drops.
	queueBuffer = 1024
)

// controlPlaneClock is the sealed, real-only clock for control-plane scheduling
// in kernel/reconcile. It is an empty package-private struct: no package outside
// kernel/reconcile can name, construct, or substitute it, so "drive the Loop's
// probe / requeue timers, or its duration measurement, with a non-real (e.g.
// fake) clock" is unrepresentable. Its three methods are the sole sanctioned
// sites for stdlib time.NewTimer / time.Now in this package.
//
// kernel/reconcile is a sanctioned control-plane host alongside runtime/command:
// PROD-CLOCK-INJECTION-01's path gate accepts both, and the (method, callee)
// pairs below are registered in that archtest's exactSanctionedTimeCalls map
// (RECONCILE-LOOP-CLOCK-CARVEOUT-01). A new method here without a matching map
// entry — or any other time.* call in a method body — is a violation.
//
// Carve-out rationale (mirrors runtime/command): control-plane scheduling and
// the framework's own duration observability must use real wall-clock time.
// Injecting a frozen fake clock with no Advance would deadlock Start (the
// startup probe never fires) and freeze every requeue.
//
// AI-robust grade: Medium (permanent ceiling). The stdlib time free functions
// cannot be made uncallable in Go, so receiver-type confinement + (method,
// callee) form-uniqueness is the achievable ceiling — identical to the
// runtime/command controlPlaneClock and the SPAN-SETATTR-REDACT-01
// package-internal axis.
type controlPlaneClock struct{}

// newProbeTimer creates a real-time timer for the startup probe window.
func (controlPlaneClock) newProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d)
}

// newRequeueTimer creates a real-time timer for a delayed requeue.
func (controlPlaneClock) newRequeueTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d)
}

// now returns the real wall-clock time, used only to measure reconcile
// duration for the duration histogram (framework observability, not business
// time — Reconcilers source their own business clock).
func (controlPlaneClock) now() time.Time {
	return time.Now()
}

// reconcilerReadinessChecker is the optional no-side-effect readiness contract a
// Reconciler may implement. Loop.Start invokes it before spawning workers so a
// misconstructed reconciler fails at OnStart (bootstrap rolls back) instead of
// erroring on every reconcile. Mirrors runtime/command.sweeperReadinessChecker.
type reconcilerReadinessChecker interface {
	Validate() error
}

// Loop is the reconcile scheduling environment: it pulls Requests from a Source,
// dispatches each to the Reconciler across a bounded worker pool (serializing
// per EntityID), and requeues per the returned Result. Its lifecycle skeleton
// (Start fast-return + startup probe, owner-ctx derivation, graceful Stop) is
// transplanted from runtime/command.SweeperLifecycle; the per-entity worker
// dispatch and requeue are reconcile-specific.
//
// Construct via the Builder (a later PR) in production; the exported fields
// support direct struct construction for tests and the kernel/command migration.
//
// Owner ctx (controller-runtime Runnable.Start semantics): Start receives the
// long-lived owner ctx and derives the workers' runCtx from it, so assembly
// shutdown (ownerCancel) drains the Loop even without an explicit Stop.
//
// Start must return promptly (spawn pool + fast probe, then return) — it is
// usable directly as a cell.LifecycleHook.OnStart; Stop as OnStop.
type Loop struct {
	// Name labels logs; defaults to "reconcile.loop".
	Name string
	// ReconcilerID is the metric/log owner dimension; when empty it defaults to
	// the "_runtime" sentinel (see reconcilerIDSentinel). When set it MUST be a
	// low-cardinality, label-safe identifier: Start rejects any value that fails
	// validateReconcilerID (lowercase [a-z0-9_], leading [a-z_], ≤48 runes) so a
	// high-cardinality or separator-bearing owner cannot blow up / corrupt the
	// reconciler metric label.
	ReconcilerID string
	// Reconciler is the required convergence callback.
	Reconciler Reconciler
	// Source feeds Requests into the Loop (a Trigger produces it in a later PR;
	// tests inject a channel directly). nil means "no external feed" — the Loop
	// still runs and self-sustains entities already seeded via requeue.
	Source <-chan Request
	// Interval is the requeue delay for Result{} (RequeueAfter == 0) and
	// transient errors; defaults to 30s.
	Interval time.Duration
	// MaxConcurrentReconciles bounds concurrent reconciles across distinct
	// EntityIDs; defaults to 1. Same-EntityID reconciles are always serial.
	MaxConcurrentReconciles int
	// StartTimeout / StopTimeout integrate with a lifecycle hook; StopTimeout is
	// consulted by Stop as the drain budget.
	StartTimeout time.Duration
	StopTimeout  time.Duration
	Logger       *slog.Logger
	// Metrics holds optional pre-bound instruments (nil-safe).
	Metrics Metrics

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// gen is the run generation, bumped under mu on each Start. Each run's
	// done-watcher captures its generation and only writes the leader gauge /
	// clears state if it is still the active generation — so a slow watcher from
	// an old run cannot reset a newer run's leader=1 (generation ownership).
	gen      uint64
	inflight sync.Map // EntityID(string) -> struct{}: same-entity serial guard
}

// Start launches the worker pool and returns once it is confirmed running.
//
// All stdlib time.* calls are funneled through the sealed controlPlaneClock.
func (l *Loop) Start(ownerCtx context.Context) error {
	if l == nil || validation.IsNilInterface(l.Reconciler) {
		return fmt.Errorf("reconcile: Loop requires non-nil Reconciler")
	}
	if rc, ok := l.Reconciler.(reconcilerReadinessChecker); ok {
		if err := rc.Validate(); err != nil {
			return fmt.Errorf("reconcile: reconciler not ready: %w", err)
		}
	}
	if err := validateReconcilerID(l.ReconcilerID); err != nil {
		return err
	}
	if err := l.Metrics.preflight(l.reconcilerID()); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil // already started — idempotent
	}
	l.gen++
	gen := l.gen

	runCtx, cancel := context.WithCancel(ownerCtx)
	queue := make(chan Request, queueBuffer)
	ready := make(chan struct{})
	var readyOnce sync.Once
	var wg sync.WaitGroup

	workers := l.maxConcurrent()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readyOnce.Do(func() { close(ready) }) // first worker confirms the pool is live
			l.runWorker(runCtx, queue, &wg)
		}()
	}
	if l.Source != nil {
		wg.Add(1)
		go l.pump(runCtx, queue, &wg)
	}

	done := make(chan struct{})
	//nolint:gosec // G118: watchDrain resets the leader gauge after the run has
	// drained (runCtx canceled by then), so its metric record must use a live ctx
	// (context.Background) rather than the dead runCtx — deliberate, see watchDrain.
	go l.watchDrain(gen, done, &wg)
	l.cancel = cancel
	l.done = done

	if err := l.awaitProbe(runCtx, cancel, ready); err != nil {
		return err
	}
	// Claim leadership only on the confirmed path. awaitProbe clears l.cancel on
	// its pre-cancel branch, so a nil l.cancel here means "owner ctx was canceled
	// before we confirmed running" — we do NOT set leader=1 then (watchDrain will
	// hold it at 0). This write is under l.mu (held for the whole Start body), so
	// it strictly precedes this run's watchDrain reset to 0, which can only
	// acquire l.mu after Start returns — closing the #1292 r2 ordering race.
	if l.cancel != nil {
		l.Metrics.setLeader(runCtx, l.reconcilerID(), 1)
		l.logger().Info("reconcile: loop started",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()),
			slog.Int("workers", workers))
	}
	return nil
}

// watchDrain waits for run gen's goroutines to finish, then — if gen is still
// the active generation — resets the leader gauge to 0 and clears run state, and
// finally closes done. It is the single site that resets leader / clears state
// on drain, covering every path uniformly: explicit Stop, owner-ctx cancel
// without Stop (assembly shutdown), and the awaitProbe pre-cancel branch.
//
// Two invariants make the leader gauge race-free (#1292 r2):
//   - Ordering: it takes l.mu, which Start holds for its entire body, so this
//     reset to 0 can only run after Start has returned — i.e. after Start's
//     confirmed-path setLeader(1). The optimistic-then-reset window that let the
//     gauge end at 1 under -count=1000 is gone.
//   - Generation ownership: the reset is gated on l.gen == gen, so a slow
//     watcher from a superseded run cannot clobber a newer run's leader=1.
//
// close(done) is sequenced after the reset, so a Stop observing <-done sees the
// gauge already at 0 (happens-before).
func (l *Loop) watchDrain(gen uint64, done chan struct{}, wg *sync.WaitGroup) {
	wg.Wait()
	l.mu.Lock()
	if l.gen == gen {
		// context.Background: the run has drained (runCtx canceled), so the
		// leader-reset record must use a live ctx. The G118 nolint sits on the
		// `go l.watchDrain(...)` launch in Start (that is where gosec reports it).
		l.Metrics.setLeader(context.Background(), l.reconcilerID(), 0)
		l.cancel = nil
		l.done = nil
	}
	l.mu.Unlock()
	close(done)
}

// pump copies Source into the internal queue until the run ctx is canceled.
func (l *Loop) pump(runCtx context.Context, queue chan<- Request, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-runCtx.Done():
			return
		case req, ok := <-l.Source:
			if !ok {
				return // Source closed — no more external feed
			}
			select {
			case queue <- req:
			case <-runCtx.Done():
				return
			}
		}
	}
}

// runWorker pulls Requests from the queue and dispatches each until canceled.
func (l *Loop) runWorker(runCtx context.Context, queue chan Request, wg *sync.WaitGroup) {
	for {
		select {
		case <-runCtx.Done():
			return
		case req := <-queue:
			l.process(runCtx, req, queue, wg)
		}
	}
}

// process reconciles one Request: it serializes per EntityID (dropping a
// duplicate while one is in flight — safe under level-triggering, since the next
// trigger / requeue re-observes), records metrics, and requeues per the result.
//
// Scope note (ADR §2.2): A3 deliberately uses skip-if-busy rather than
// controller-runtime's dirty/processing dedup. A coalesced trigger is NOT lost
// under level-triggering — the next resync / requeue re-observes the latest
// state — but a "mark-dirty, re-run once after the in-flight reconcile" queue
// (so convergence does not wait for the next resync) is deferred to PR-A5
// (#1166, F5) — accepted deferral, not yet implemented.
func (l *Loop) process(runCtx context.Context, req Request, queue chan<- Request, wg *sync.WaitGroup) {
	if _, busy := l.inflight.LoadOrStore(req.EntityID, struct{}{}); busy {
		l.Metrics.recordResult(runCtx, l.reconcilerID(), resultSkipped)
		return
	}
	defer l.inflight.Delete(req.EntityID)

	l.Metrics.inFlightDelta(runCtx, l.reconcilerID(), 1)
	defer l.Metrics.inFlightDelta(runCtx, l.reconcilerID(), -1)

	start := controlPlaneClock{}.now()
	res, err := l.safeReconcile(runCtx, req)
	l.Metrics.observeDuration(runCtx, l.reconcilerID(), controlPlaneClock{}.now().Sub(start).Seconds())

	switch {
	case err == nil:
		l.Metrics.recordResult(runCtx, l.reconcilerID(), resultSuccess)
		l.scheduleRequeue(runCtx, req, res.normalizedRequeueAfter(), queue, wg)
	case IsPermanent(err):
		l.Metrics.recordResult(runCtx, l.reconcilerID(), resultPermanent)
		l.logger().Error("reconcile: permanent error (dead-letter; not requeued)",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()),
			slog.String("entity", req.EntityID),
			slog.Any("error", redaction.RedactError(err)))
		// Permanent: do NOT requeue. A fresh Source trigger re-observes it if the
		// consumer resets the entity's state.
	default:
		l.Metrics.recordResult(runCtx, l.reconcilerID(), resultTransient)
		l.logger().Error("reconcile: transient error (requeued)",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()),
			slog.String("entity", req.EntityID),
			slog.Any("error", redaction.RedactError(err)))
		// Transient: requeue at the default interval. Exponential backoff is the
		// backoff/recovery PR's refinement.
		l.scheduleRequeue(runCtx, req, 0, queue, wg)
	}
}

// safeReconcile invokes the Reconciler with panic recovery so a single entity's
// panic cannot crash the worker goroutine (and the process). A recovered panic
// is reported as a transient error (retryable); a dedicated panic disposition /
// taxonomy is the backoff/recovery PR's concern.
func (l *Loop) safeReconcile(ctx context.Context, req Request) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reconcile: recovered panic in Reconcile(entity=%q): %v", req.EntityID, r)
			res = Result{}
		}
	}()
	return l.Reconciler.Reconcile(ctx, req)
}

// scheduleRequeue re-enqueues req after the given delay (0 → default Interval)
// via a goroutine bounded by the run ctx. The goroutine is tracked by wg so Stop
// waits for it, and exits immediately on cancellation (no leak, no requeue into
// a draining Loop).
//
// Scope note (ADR §2.2, threat T-LEAK): A3 spawns one short-lived timer
// goroutine per requeue — simpler than client-go's shared delaying queue, and
// leak-free (every goroutine is runCtx-derived + WaitGroup-tracked). The
// concurrent count is bounded by the live entity set, not capped; a shared
// rate-limited delaying queue (one timer goroutine total, exponential backoff)
// is deferred to PR-A5 (#1166, F6) — accepted deferral, not yet implemented.
func (l *Loop) scheduleRequeue(runCtx context.Context, req Request, after time.Duration, queue chan<- Request, wg *sync.WaitGroup) {
	if after <= 0 {
		after = l.interval()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := controlPlaneClock{}.newRequeueTimer(after)
		defer timer.Stop()
		select {
		case <-runCtx.Done():
			return
		case <-timer.C:
		}
		select {
		case queue <- req:
		case <-runCtx.Done():
		}
	}()
}

// awaitProbe waits for the worker pool to confirm it is running, returning nil
// in all non-error cases:
//   - owner ctx already canceled before we got here: log Warn, clear l.cancel so
//     Start skips the leader=1 claim (and a later Stop is a no-op), and let the
//     workers self-exit via runCtx.Done(). Checked FIRST (before the select) so
//     this path is deterministic — `ready` closes at worker spawn and would
//     otherwise race the cancellation, leaving this branch ~unreachable (and
//     untestable).
//   - ready closed: a worker started — confirmed.
//   - probe window elapsed: defensive fallback if a worker is slow to schedule;
//     workers are spawned, so return anyway (fast-return OnStart contract).
//
// Runs under Start's l.mu, so clearing l.cancel/l.done here needs no extra lock.
func (l *Loop) awaitProbe(runCtx context.Context, cancel context.CancelFunc, ready <-chan struct{}) error {
	if runCtx.Err() != nil {
		l.logger().Warn("reconcile: owner ctx canceled before loop confirmed running",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()))
		cancel()
		// Clear l.cancel so Start's post-probe check skips the leader=1 claim:
		// the gauge is never raised on this path, so there is nothing to reset
		// and no setLeader(1)/setLeader(0) ordering race. watchDrain still runs
		// (gen-guarded) and leaves the gauge at 0.
		l.cancel = nil
		l.done = nil
		return nil
	}

	probeTimer := controlPlaneClock{}.newProbeTimer(startProbeTimeout)
	defer probeTimer.Stop()
	select {
	case <-ready:
	case <-probeTimer.C:
	}
	return nil
}

// Stop cancels the Loop and waits for all goroutines (workers, pump, pending
// requeues) to exit within ctx's budget.
//
// State (l.cancel / l.done) is cleared by watchDrain ONLY after the goroutines
// actually drain. Until then the fields stay set so that (a) a concurrent Start
// remains a no-op — it never spawns a second pool racing the one still unwinding
// — and (b) a Stop that exhausts ctx's budget can simply be called again to keep
// waiting. cancel() is idempotent, so a retried Stop re-selects on the same done
// channel without harm. The leader gauge is reset to 0 by watchDrain (sequenced
// before close(done)), so by the time this Stop observes <-done the gauge is
// already 0. A timed-out Stop is "not yet stopped", so leader stays 1 until a
// later Stop (or owner-ctx cancel) drains it.
func (l *Loop) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.mu.Unlock()

	if cancel == nil {
		return nil // never started, or already fully stopped
	}
	cancel()

	select {
	case <-done:
		// Drained: watchDrain has already reset the leader gauge to 0 and cleared
		// l.cancel/l.done (sequenced before close(done)), so a later Start can
		// restart the loop. Nothing left to do but log.
		l.logger().Info("reconcile: loop stopped",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()))
		return nil
	case <-ctx.Done():
		// Budget exhausted before drain. cancel() has fired so the goroutines
		// are unwinding; leave l.cancel/l.done intact so Stop is retryable and
		// Start cannot race the draining pool.
		return ctx.Err()
	}
}

func (l *Loop) maxConcurrent() int {
	if l.MaxConcurrentReconciles > 0 {
		return l.MaxConcurrentReconciles
	}
	return defaultMaxConcurrentReconciles
}

func (l *Loop) interval() time.Duration {
	if l.Interval > 0 {
		return l.Interval
	}
	return defaultReconcileInterval
}

func (l *Loop) reconcilerID() string {
	if l != nil && l.ReconcilerID != "" {
		return l.ReconcilerID
	}
	return reconcilerIDSentinel
}

// maxReconcilerIDLen bounds a ReconcilerID, mirroring the owner-dimension length
// cap used by adapters/redis KeyNamespace and kernel/healthz ProbeName.
const maxReconcilerIDLen = 48

// validateReconcilerID rejects a non-empty ReconcilerID that is not a
// low-cardinality, label-safe owner identifier. Empty is allowed (Start then
// uses the reconcilerIDSentinel). The accepted shape — leading [a-z_], body
// [a-z0-9_], ≤ maxReconcilerIDLen runes — mirrors the cell-id / ProbeName /
// KeyNamespace owner conventions: lowercase keeps the metric/log dimension
// stable, the charset excludes the adapter label separators ('|', '=') plus
// whitespace / control bytes, and the length cap bounds cardinality. The
// reconciler metric label is a registration-time owner dimension (set by the
// Loop's constructor, not request input), so this is a fail-fast guard against a
// misconfigured owner rather than untrusted-input sanitization. It runs at Start
// before the metric preflight so a bad ID surfaces at OnStart (bootstrap rolls
// back) instead of corrupting the time-series.
func validateReconcilerID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxReconcilerIDLen {
		return fmt.Errorf("reconcile: ReconcilerID %q exceeds %d bytes", id, maxReconcilerIDLen)
	}
	for i, r := range id {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("reconcile: ReconcilerID %q has illegal character %q "+
				"(allowed: leading [a-z_], then [a-z0-9_])", id, r)
		}
	}
	return nil
}

func (l *Loop) name() string {
	if l != nil && l.Name != "" {
		return l.Name
	}
	return defaultLoopName
}

func (l *Loop) logger() *slog.Logger {
	if l != nil && l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}
