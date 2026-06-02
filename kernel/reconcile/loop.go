package reconcile

import (
	"container/heap"
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
	// returns Result{} (RequeueAfter == 0).
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
	// waitingQueueIdleDelay is the "never" sentinel the delaying-queue timer
	// sleeps for when the heap is empty; it is reset to a real readyAt as soon
	// as an item arrives. Mirrors client-go delaying_queue.go maxWait.
	//
	// ref: kubernetes/client-go util/workqueue/delaying_queue.go
	waitingQueueIdleDelay = 24 * time.Hour
	// queueBuffer sizes the internal work queue. Source and requeue both feed
	// it; backpressure (a full buffer) blocks the producer, never drops.
	queueBuffer = 1024
	// addChBuffer sizes the channel from process() to waitingLoop. One slot per
	// requeue call is sufficient since senders block on the channel write; a
	// small buffer reduces lock contention under bursts.
	addChBuffer = 256
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

// newRequeueTimer creates a real-time timer for a delayed requeue (used by the
// shared waitingLoop as its single heap-driven sleep timer).
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

// waitingItem is a pending requeue entry in the delaying-queue heap.
// ref: kubernetes/client-go util/workqueue/delaying_queue.go
type waitingItem struct {
	req     Request
	readyAt time.Time
	// index is maintained by the heap interface for heap.Fix.
	index int
}

// waitingHeap is a min-heap of *waitingItem ordered by readyAt (earliest first).
// ref: kubernetes/client-go util/workqueue/delaying_queue.go
type waitingHeap []*waitingItem

func (h waitingHeap) Len() int           { return len(h) }
func (h waitingHeap) Less(i, j int) bool { return h[i].readyAt.Before(h[j].readyAt) }
func (h waitingHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *waitingHeap) Push(x any) {
	it := x.(*waitingItem)
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *waitingHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	it.index = -1
	*h = old[:n-1]
	return it
}

// Loop is the reconcile scheduling environment: it pulls Requests from a Source,
// dispatches each to the Reconciler across a bounded worker pool (serializing
// per EntityID), and requeues per the returned Result. Its lifecycle skeleton
// (Start fast-return + startup probe, owner-ctx derivation, graceful Stop) is
// transplanted from runtime/command.SweeperLifecycle; the per-entity worker
// dispatch and requeue are reconcile-specific.
//
// Construct via the Builder (PR-A7, not yet landed) in production; the exported
// fields support direct struct construction for tests and the kernel/command migration.
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
	// validateReconcilerID (lowercase [a-z0-9_], leading [a-z_], ≤48 bytes
	// (ASCII-only)) so a
	// high-cardinality or separator-bearing owner cannot blow up / corrupt the
	// reconciler metric label.
	ReconcilerID string
	// Reconciler is the required convergence callback.
	Reconciler Reconciler
	// Source feeds Requests into the Loop (a Trigger, PR-A4, produces it;
	// tests inject a channel directly). nil means "no external feed" — the Loop
	// still runs and self-sustains entities already seeded via requeue.
	Source <-chan Request
	// Interval is the requeue delay for Result{} (RequeueAfter == 0);
	// defaults to 30s.
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

	// BaseDelay is the initial (first-retry) backoff delay for transient errors.
	// Zero or negative means use the default of 5ms (mirroring client-go
	// ItemExponentialFailureRateLimiter). The delay doubles on each consecutive
	// transient failure for the same entity, capped at MaxDelay.
	//
	// BaseDelay governs ONLY the transient-error exponential backoff — it does
	// NOT affect the success-path Interval requeue.
	//
	// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
	BaseDelay time.Duration
	// MaxDelay is the cap on the transient-error backoff delay for a single
	// entity. Zero or negative means use the default of 1000s (mirroring
	// client-go ItemExponentialFailureRateLimiter). After MaxDelay is reached, retries
	// continue at MaxDelay until the entity succeeds (backoff.Forget) or is
	// dead-lettered (permanent error).
	//
	// MaxDelay governs ONLY the transient-error exponential backoff — it does
	// NOT affect the success-path Interval requeue.
	//
	// ref: kubernetes/client-go util/workqueue/default_rate_limiters.go
	MaxDelay time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// gen is the run generation, bumped under mu on each Start. Each run's
	// done-watcher captures its generation and only writes the leader gauge /
	// clears state if it is still the active generation — so a slow watcher from
	// an old run cannot reset a newer run's leader=1 (generation ownership).
	gen uint64

	// entityMu guards processing and dirty — the F5 dirty/processing maps.
	//
	// Concurrency invariants (DX2) — read this before touching process():
	//
	//  (1) What entityMu protects: the processing map (entities currently being
	//      reconciled) and the dirty map (coalesced latest trigger per entity
	//      received while that entity is in-flight). No other state.
	//
	//  (2) Lost-wakeup-safety: a trigger arriving during in-flight processing
	//      EITHER sets dirty (→ guaranteed single re-run on completion) OR, if
	//      it races past the completion window and the entity is no longer marked
	//      processing, lands as a fresh enqueue via the normal queue path. A
	//      trigger is NEVER silently dropped.
	//
	//  (3) Atomicity requirement: the dirty read+clear and the processing-marker
	//      delete MUST happen atomically under entityMu in the completion path
	//      (process's post-reconcile block). Separating these two operations
	//      opens a window where a new trigger arrives after dirty is cleared but
	//      before processing is deleted: it sees processing=true, sets dirty again,
	//      but the completion path already read dirty and won't re-enqueue it —
	//      a lost-wakeup. Keep them in the same Lock/Unlock block.
	entityMu   sync.Mutex
	processing map[string]bool    // entities currently being reconciled
	dirty      map[string]Request // coalesced dirty trigger per entity
}

// Start launches the worker pool and returns once it is confirmed running.
//
// All stdlib time.* calls are funneled through the sealed controlPlaneClock.
func (l *Loop) Start(ownerCtx context.Context) error {
	if err := l.preStartValidate(); err != nil {
		return err
	}
	return l.start(ownerCtx)
}

// preStartValidate runs the fail-fast checks that must pass before Start creates
// any per-run state or spawns a goroutine. Safe on a nil receiver (the nil check
// is first). Kept separate from start() so Start stays within the funlen budget.
func (l *Loop) preStartValidate() error {
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
	// Fail fast on an inverted backoff window. Both must be explicitly set
	// (>0) to be a misconfiguration; a zero field means "use the default"
	// (newEntityBackoff fills it in). With base > max the first When would
	// already return max, silently violating the documented [base, max]
	// interval — surface it as a Start error instead.
	if l.BaseDelay > 0 && l.MaxDelay > 0 && l.BaseDelay > l.MaxDelay {
		return fmt.Errorf("reconcile: BaseDelay (%s) must not exceed MaxDelay (%s)", l.BaseDelay, l.MaxDelay)
	}
	return l.Metrics.preflight(l.reconcilerID())
}

// start performs the run setup (per-run state, worker pool, delaying queue,
// startup probe) after preStartValidate has passed.
func (l *Loop) start(ownerCtx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil // already started — idempotent
	}
	l.gen++
	gen := l.gen

	// Initialize per-run entity state.
	l.entityMu.Lock()
	l.processing = make(map[string]bool)
	l.dirty = make(map[string]Request)
	l.entityMu.Unlock()

	runCtx, cancel := context.WithCancel(ownerCtx)
	queue := make(chan Request, queueBuffer)
	addCh := make(chan waitingItem, addChBuffer)
	// cancelCh carries EntityIDs whose pending requeue must be removed from the
	// delaying queue (permanent dead-letter). Same buffer/lifecycle as addCh.
	cancelCh := make(chan string, addChBuffer)
	ready := make(chan struct{})
	var readyOnce sync.Once
	var wg sync.WaitGroup

	// Backoff state for this run.
	backoff := newEntityBackoff(l.BaseDelay, l.MaxDelay)

	workers := l.maxConcurrent()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readyOnce.Do(func() { close(ready) }) // first worker confirms the pool is live
			l.runWorker(runCtx, queue, addCh, cancelCh, backoff)
		}()
	}
	if l.Source != nil {
		wg.Add(1)
		go l.feedFromSource(runCtx, queue, &wg)
	}

	// F6: single shared delaying queue — ONE goroutine, ONE timer.
	// ref: kubernetes/client-go util/workqueue/delaying_queue.go
	wg.Add(1)
	go l.waitingLoop(runCtx, addCh, cancelCh, queue, &wg)

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

// drainReadyItems pops all items from h whose readyAt is not after now and
// pushes each Request into queue, removing each popped entity from pending (the
// EntityID→item index that addOrMergeWaiting maintains). Returns false if runCtx
// was canceled.
//
// Paired with enqueueDelayed, which is the sole path that adds items to the heap
// via the addCh channel consumed by waitingLoop.
//
// ref: kubernetes/client-go util/workqueue/delaying_queue.go
func drainReadyItems(runCtx context.Context, h *waitingHeap, pending map[string]*waitingItem, now time.Time, queue chan<- Request) bool {
	for h.Len() > 0 && !(*h)[0].readyAt.After(now) {
		it := heap.Pop(h).(*waitingItem)
		delete(pending, it.req.EntityID)
		select {
		case queue <- it.req:
		case <-runCtx.Done():
			return false
		}
	}
	return true
}

// addOrMergeWaiting inserts item into the delaying-queue heap, or — when an item
// for the same EntityID is already waiting — keeps whichever readyAt is earlier
// (heap.Fix repositions the existing entry). At most one heap entry exists per
// distinct entity, so a repeat requeue cannot grow the heap and a sooner
// (delay=0 dirty re-run) supersedes a later (interval/backoff) entry.
//
// ref: kubernetes/client-go util/workqueue/delaying_queue.go (waitingForMap merge)
func addOrMergeWaiting(h *waitingHeap, pending map[string]*waitingItem, item waitingItem) {
	if existing, ok := pending[item.req.EntityID]; ok {
		if item.readyAt.Before(existing.readyAt) {
			existing.readyAt = item.readyAt
			heap.Fix(h, existing.index)
		}
		return
	}
	it := item // copy to take a stable address shared by heap and pending
	heap.Push(h, &it)
	pending[item.req.EntityID] = &it
}

// cancelPending removes any pending requeue for entityID from the delaying-queue
// heap (permanent dead-letter: the entity must not be re-reconciled by a stale
// pre-existing requeue). No-op when the entity has no pending item.
func cancelPending(h *waitingHeap, pending map[string]*waitingItem, entityID string) {
	existing, ok := pending[entityID]
	if !ok {
		return
	}
	heap.Remove(h, existing.index)
	delete(pending, entityID)
}

// nextWaitingDelay returns how long to sleep until the earliest item in h is
// ready. Returns the idle sentinel (waitingQueueIdleDelay) when h is empty.
//
// ref: kubernetes/client-go util/workqueue/delaying_queue.go
func nextWaitingDelay(h *waitingHeap, now time.Time) time.Duration {
	if h.Len() == 0 {
		return waitingQueueIdleDelay
	}
	d := (*h)[0].readyAt.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// resetTimer drains the timer channel if needed and resets it to d.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// waitingLoop is the single shared delaying queue goroutine (F6).
// It maintains a min-heap of pending requeue items and uses ONE reusable timer
// (from controlPlaneClock{}.newRequeueTimer) to sleep until the earliest readyAt.
// When an item is ready, it pushes the Request into the worker queue.
//
// On runCtx cancellation it exits cleanly (goleak-clean): pending items are
// abandoned (the run is shutting down).
//
// ref: kubernetes/client-go util/workqueue/delaying_queue.go waitingLoop
func (l *Loop) waitingLoop(
	runCtx context.Context,
	addCh <-chan waitingItem,
	cancelCh <-chan string,
	queue chan<- Request,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	var h waitingHeap
	heap.Init(&h)

	// pending indexes the heap by EntityID so a repeat requeue for an entity
	// already waiting MERGES into the existing item (keeping the earlier readyAt)
	// rather than pushing a duplicate. This bounds the heap to one entry per
	// distinct waiting entity and gives client-go's "only update if sooner"
	// convergence: a dirty re-run at delay=0 supersedes a later interval/backoff
	// entry for the same entity instead of leaving two heap entries.
	//
	// ref: kubernetes/client-go util/workqueue/delaying_queue.go (waitingForMap)
	pending := make(map[string]*waitingItem)

	clk := controlPlaneClock{}
	timer := clk.newRequeueTimer(waitingQueueIdleDelay) // sentinel: empty heap
	defer timer.Stop()

	for {
		if !drainReadyItems(runCtx, &h, pending, clk.now(), queue) {
			return
		}

		resetTimer(timer, nextWaitingDelay(&h, clk.now()))

		select {
		case <-runCtx.Done():
			return
		case item := <-addCh:
			addOrMergeWaiting(&h, pending, item)
		case id := <-cancelCh:
			cancelPending(&h, pending, id)
		case <-timer.C:
			// Timer fired — loop back to drain ready items.
		}
	}
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

// feedFromSource copies Source into the internal queue until the run ctx is canceled.
func (l *Loop) feedFromSource(runCtx context.Context, queue chan<- Request, wg *sync.WaitGroup) {
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
func (l *Loop) runWorker(
	runCtx context.Context,
	queue chan Request,
	addCh chan<- waitingItem,
	cancelCh chan<- string,
	backoff *entityBackoff,
) {
	for {
		select {
		case <-runCtx.Done():
			return
		case req := <-queue:
			l.process(runCtx, req, addCh, cancelCh, backoff)
		}
	}
}

// process reconciles one Request: it serializes per EntityID (F5: dirty/processing
// dedup — a duplicate arriving during an in-flight reconcile is coalesced into
// a single dirty re-run, not silently dropped), records metrics, and requeues
// per the result via the shared delaying queue (F6).
//
// F5 dirty/processing dedup: replaces the old inflight sync.Map skip-if-busy with
// explicit processing/dirty state under entityMu. A duplicate trigger arriving
// while one is in flight sets dirty=true (coalesced into ONE re-run); the
// re-run is enqueued at delay 0 immediately after the in-flight reconcile
// completes. This is level-triggered convergence: the re-run observes the latest
// state regardless of how many duplicates were coalesced.
//
// Lost-wakeup safety: {set-processing-on-entry}, {clear-dirty+remove-processing
// on completion}, and {check-processing+set-dirty on duplicate arrival} are all
// atomic under entityMu, so a trigger arriving exactly as process finishes is
// either seen as dirty→re-run or lands as a fresh enqueue — never silently dropped.
//
// Scope note (ADR §2.2): F5+F6 are the PR-A5 features that complete the
// controller-runtime dirty/processing + rate-limited delaying queue equivalence.
func (l *Loop) process(runCtx context.Context, req Request, addCh chan<- waitingItem, cancelCh chan<- string, backoff *entityBackoff) {
	// F5: check/set processing under entityMu.
	l.entityMu.Lock()
	if l.processing[req.EntityID] {
		// Entity is in flight — coalesce this trigger as dirty (latest wins).
		l.dirty[req.EntityID] = req
		l.entityMu.Unlock()
		l.Metrics.recordResult(runCtx, l.reconcilerID(), resultSkipped)
		return
	}
	l.processing[req.EntityID] = true
	l.entityMu.Unlock()

	l.Metrics.inFlightDelta(runCtx, l.reconcilerID(), 1)
	defer l.Metrics.inFlightDelta(runCtx, l.reconcilerID(), -1)

	start := controlPlaneClock{}.now()
	res, err := recoverReconcile(runCtx, l.Reconciler, req, l.logger(), l.reconcilerID())
	l.Metrics.observeDuration(runCtx, l.reconcilerID(), controlPlaneClock{}.now().Sub(start).Seconds())

	label := classify(err)
	l.Metrics.recordResult(runCtx, l.reconcilerID(), label)

	l.dispatchResult(runCtx, req, res, err, label, addCh, cancelCh, backoff)

	// F5: clear processing and check dirty under entityMu.
	l.entityMu.Lock()
	dirtyReq, wasDirty := l.dirty[req.EntityID]
	delete(l.dirty, req.EntityID)
	delete(l.processing, req.EntityID)
	l.entityMu.Unlock()

	if wasDirty && label != resultPermanent {
		// Re-enqueue the coalesced dirty trigger immediately (delay=0).
		// This is a fresh convergence run, not a backoff retry.
		//
		// dispatchResult already enqueued the normal success/transient requeue
		// above (at the Interval or backoff delay). addOrMergeWaiting MERGES this
		// delay=0 re-run with that later entry for the same entity (earlier
		// readyAt wins), so the entity ends up with ONE heap entry that fires
		// immediately as the convergence run — not two. Convergence is preserved
		// without leaving a duplicate pending item.
		//
		// Suppressed on resultPermanent: a permanent dead-letter must NOT be
		// re-reconciled. The dispatchResult permanent branch already canceled the
		// entity's pending item (cancelCh); re-running the in-flight dirty trigger
		// here would resurrect it and race the cancel across two channels. A fresh
		// Source trigger re-observes the entity if the consumer resets its state.
		l.enqueueDelayed(runCtx, dirtyReq, 0, addCh)
	}
}

// dispatchResult routes the reconcile outcome to the appropriate requeue action.
func (l *Loop) dispatchResult(
	runCtx context.Context,
	req Request,
	res Result,
	err error,
	label resultLabel,
	addCh chan<- waitingItem,
	cancelCh chan<- string,
	backoff *entityBackoff,
) {
	switch label {
	case resultSuccess:
		backoff.Forget(req.EntityID)
		delay := res.normalizedRequeueAfter()
		if delay <= 0 {
			delay = l.interval()
		}
		l.enqueueDelayed(runCtx, req, delay, addCh)
	case resultPermanent:
		backoff.Forget(req.EntityID)
		// Cancel any pending requeue for this entity: an earlier success/transient
		// may have enqueued a (possibly long) Interval/backoff item that, left in
		// place, would re-reconcile a now-dead-lettered entity once it fires. A
		// fresh Source trigger re-observes it if the consumer resets its state.
		l.enqueueCancel(runCtx, req.EntityID, cancelCh)
		l.logger().Error("reconcile: permanent error (dead-letter; not requeued)",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()),
			slog.String("entity", req.EntityID),
			slog.Any("error", redaction.RedactError(err)))
	default: // resultTransient (including recovered panics)
		delay := backoff.When(req.EntityID)
		l.logger().Warn("reconcile: transient error (requeued with backoff)",
			slog.String("loop", l.name()),
			slog.String("reconciler", l.reconcilerID()),
			slog.String("entity", req.EntityID),
			slog.Duration("backoff_delay", delay),
			slog.Any("error", redaction.RedactError(err)))
		l.enqueueDelayed(runCtx, req, delay, addCh)
	}
}

// enqueueDelayed sends a waitingItem to the shared delaying queue.
// If delay <= 0 the item is considered immediately ready (readyAt = now).
// Respects runCtx cancellation so a shutting-down Loop doesn't leak goroutines.
//
// Paired with drainReadyItems, which moves ready items from the heap to the
// worker queue inside waitingLoop.
func (l *Loop) enqueueDelayed(runCtx context.Context, req Request, delay time.Duration, addCh chan<- waitingItem) {
	readyAt := controlPlaneClock{}.now()
	if delay > 0 {
		readyAt = readyAt.Add(delay)
	}
	item := waitingItem{req: req, readyAt: readyAt}
	select {
	case addCh <- item:
	case <-runCtx.Done():
	}
}

// enqueueCancel asks waitingLoop to remove entityID's pending requeue (permanent
// dead-letter). Sole funnel from the worker path into cancelCh; respects runCtx
// cancellation. Paired with cancelPending, which performs the heap removal inside
// waitingLoop.
func (l *Loop) enqueueCancel(runCtx context.Context, entityID string, cancelCh chan<- string) {
	select {
	case cancelCh <- entityID:
	case <-runCtx.Done():
	}
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

// Stop cancels the Loop and waits for all goroutines (workers, feedFromSource, waitingLoop)
// to exit within ctx's budget.
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
