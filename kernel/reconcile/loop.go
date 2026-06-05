package reconcile

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
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
	// the HTTP metrics cell label's `_runtime` sentinel) so owner-dimension
	// filters stay consistent across metrics — e.g. a dashboard can exclude
	// unowned series with reconciler!="_runtime" exactly as it does
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
	// defaultRenewInterval is the renew-cadence floor used when neither an
	// explicit RenewInterval nor a positive token TTL is available. 5s ≈ 15s/3
	// mirrors client-go leaderelection (LeaseDuration 15s, RenewDeadline 10s).
	// ref: kubernetes/client-go tools/leaderelection/leaderelection.go
	defaultRenewInterval = 5 * time.Second
	// leaderRetryPeriod is how long leaderManage waits after a failed/contended
	// AcquireLease before retrying (follower poll). It bounds the graceful-handoff
	// RTO: after a leader's ReleaseLease frees the lease, a follower acquires on its
	// next poll, so this is the SC-004 "graceful shutdown → follower P99 ≤ 1s" knob
	// (PR-A6 review C5/F8). 1s (shorter than client-go's 2s RetryPeriod) honors that
	// ≤1s promise; crash failover is separately bounded by LeaseDuration + this poll.
	// ref: kubernetes/client-go tools/leaderelection/leaderelection.go (RetryPeriod)
	leaderRetryPeriod = 1 * time.Second
	// leaseReleaseTimeout bounds the best-effort ReleaseLease attempt on shutdown
	// so a hung backend cannot stall Stop; the lease then expires on its TTL.
	leaseReleaseTimeout = 5 * time.Second
	// renewIntervalDivisor derives the default renew cadence as TTL/3 (mirrors the
	// client-go 15s/5s ratio). Named so the literal never appears inside a
	// Duration-typed expression (PROD-DURATION-CONST-01).
	renewIntervalDivisor = 3
)

// controlPlaneClock is the sealed, real-only clock for control-plane scheduling
// in kernel/reconcile. It is an empty package-private struct: no package outside
// kernel/reconcile can name, construct, or substitute it, so "drive the Loop's
// probe / requeue timers, or its duration measurement, with a non-real (e.g.
// fake) clock" is unrepresentable. Its four methods are the sole sanctioned
// sites for stdlib time.NewTimer / time.NewTicker / time.Now in this package;
// newRenewTicker handles the lease renew cadence ticker.
//
// kernel/reconcile is the sanctioned control-plane host:
// PROD-CLOCK-INJECTION-01's path gate accepts it, and the (method, callee)
// pairs below are registered in that archtest's exactSanctionedTimeCalls map
// (RECONCILE-LOOP-CLOCK-CARVEOUT-01). A new method here without a matching map
// entry — or any other time.* call in a method body — is a violation.
//
// Carve-out rationale: control-plane scheduling and the framework's own
// duration observability must use real wall-clock time. Injecting a frozen
// fake clock with no Advance would deadlock Start (the startup probe never
// fires) and freeze every requeue.
//
// AI-robust grade: Medium (permanent ceiling). The stdlib time free functions
// cannot be made uncallable in Go, so receiver-type confinement + (method,
// callee) form-uniqueness is the achievable ceiling — identical to the
// SPAN-SETATTR-REDACT-01 package-internal axis.
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

// newRenewTicker creates a real-time ticker for the leader lease renew cadence.
// Lease renewal is control-plane scheduling: a frozen fake clock with no Advance
// would freeze renewal and let the lease lapse, so it must use real wall time
// (distinct from TickerTrigger's injected business clock). The (newRenewTicker,
// time.NewTicker) pair is registered in PROD-CLOCK-INJECTION-01's
// exactSanctionedTimeCalls map (RECONCILE-LOOP-CLOCK-CARVEOUT-01).
func (controlPlaneClock) newRenewTicker(d time.Duration) *time.Ticker {
	return time.NewTicker(d)
}

// reconcilerReadinessChecker is the optional no-side-effect readiness contract a
// Reconciler may implement. Loop.Start invokes it before spawning workers so a
// misconstructed reconciler fails at OnStart (bootstrap rolls back) instead of
// erroring on every reconcile. Implemented via the reconcilerReadinessChecker seam.
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

// Loop is the reconcile scheduling environment: it pulls Requests from a source,
// dispatches each to the Reconciler across a bounded worker pool (serializing
// per EntityID), and requeues per the returned Result. Its lifecycle skeleton
// (Start fast-return + startup probe, owner-ctx derivation, graceful Stop) is
// the scheduling loop's own control shell; the per-entity worker dispatch and
// requeue are reconcile-specific.
//
// Construct via the Builder (reconcile.New(r).With*().Build()) in production.
// All configuration fields are unexported; the Builder is the sole public
// construction entry point. Loop config fields are private so that
// &reconcile.Loop{Field: ...} from outside the package is a compile error —
// the funnel is enforced by the type system (Hard upstream).
//
// Defaulting is handled by a single applyDefaults() called once at the end of
// preStartValidate(), before the metric preflight, so start() sees all config
// fields at their final values.
//
// Owner ctx (controller-runtime Runnable.Start semantics): Start receives the
// long-lived owner ctx and derives the workers' runCtx from it, so assembly
// shutdown (ownerCancel) drains the Loop even without an explicit Stop.
//
// Start must return promptly (spawn pool + fast probe, then return) — it is
// usable directly as a cell.LifecycleHook.OnStart; Stop as OnStop.
//
// ref: kubernetes-sigs/controller-runtime pkg/builder/controller.go
type Loop struct {
	// name labels logs; defaults to defaultLoopName via applyDefaults.
	name string
	// reconcilerID is the metric/log owner dimension; when empty it defaults to
	// the "_runtime" sentinel via applyDefaults. When set it MUST be a
	// low-cardinality, label-safe identifier: Start rejects any value that fails
	// validateReconcilerID (lowercase [a-z0-9_], leading [a-z_], ≤48 bytes).
	reconcilerID string
	// reconciler is the required convergence callback.
	reconciler Reconciler
	// source feeds Requests into the Loop. Set by the Builder to triggerCh.
	// nil means "no external feed" — the Loop still runs and self-sustains
	// entities already seeded via requeue.
	source <-chan Request
	// trigger is the Trigger wired by the Builder; started in start().
	trigger Trigger
	// triggerCh is the bidirectional channel created by Build; trigger writes
	// into it, source (read end) feeds the work queue.
	triggerCh chan Request
	// interval is the requeue delay for Result{} (RequeueAfter == 0);
	// defaults to defaultReconcileInterval via applyDefaults.
	interval time.Duration
	// maxConcurrentReconciles bounds concurrent reconciles across distinct
	// EntityIDs; defaults to defaultMaxConcurrentReconciles via applyDefaults.
	maxConcurrentReconciles int
	// logger defaults to slog.Default() via applyDefaults. There is no WithLogger
	// Builder option by design: all logging routes through the process-global
	// sealed slog sink (runtime/observability/logging) for fail-closed redaction —
	// a per-loop raw logger would bypass it. See builder.go / observability.md.
	logger *slog.Logger
	// metrics holds optional pre-bound instruments (nil-safe).
	metrics Metrics

	// baseDelay is the initial backoff delay for transient errors.
	// Zero → default 5ms (mirroring client-go ItemExponentialFailureRateLimiter).
	baseDelay time.Duration
	// maxDelay is the cap on transient-error backoff delay per entity.
	// Zero → default 1000s (mirroring client-go ItemExponentialFailureRateLimiter).
	maxDelay time.Duration

	// leader, when non-nil, gates the whole loop to the lease holder.
	// nil = single-process mode (always leader, Epoch 0, no fencing).
	leader LeaderElector
	// fencedRepo, when non-nil, is the epoch-aware write seam.
	fencedRepo FencedRepository
	// renewInterval optionally overrides the lease renew cadence.
	// Zero → derived as (ExpiresAt-AcquiredAt)/3 from the acquired token.
	renewInterval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	// currentLease holds the live LeaseToken while this replica is the leader
	// (nil otherwise). process() reads its Epoch via currentEpoch() to mint the
	// per-Reconcile FencedWriter. Atomic so the worker pool reads it without
	// taking l.mu; the leaderManage goroutine is the sole writer.
	currentLease atomic.Pointer[LeaseToken]
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
	if l == nil || validation.IsNilInterface(l.reconciler) {
		return fmt.Errorf("reconcile: Loop requires non-nil Reconciler")
	}
	if rc, ok := l.reconciler.(reconcilerReadinessChecker); ok {
		if err := rc.Validate(); err != nil {
			return fmt.Errorf("reconcile: reconciler not ready: %w", err)
		}
	}
	if err := validateReconcilerID(l.reconcilerID); err != nil {
		return err
	}
	// Fail fast on an inverted backoff window. Both must be explicitly set
	// (>0) to be a misconfiguration; a zero field means "use the default"
	// (newEntityBackoff fills it in). With base > max the first When would
	// already return max, silently violating the documented [base, max]
	// interval — surface it as a Start error instead.
	if l.baseDelay > 0 && l.maxDelay > 0 && l.baseDelay > l.maxDelay {
		return fmt.Errorf("reconcile: BaseDelay (%s) must not exceed MaxDelay (%s)", l.baseDelay, l.maxDelay)
	}
	// Typed-nil fail-fast (PR-A6 review C5/F11): a typed-nil interface stored in
	// leader/fencedRepo is != nil, so it would slip past the leader==nil mode gate
	// and panic inside leaderManage / process (a goroutine) rather than at Start.
	// Surface it here so a misconstructed Loop fails at OnStart (bootstrap rolls back).
	if l.leader != nil && validation.IsNilInterface(l.leader) {
		return fmt.Errorf("reconcile: Loop.Leader is a typed-nil LeaderElector; leave it nil for single-process mode")
	}
	if l.fencedRepo != nil && validation.IsNilInterface(l.fencedRepo) {
		return fmt.Errorf("reconcile: Loop.FencedRepo is a typed-nil FencedRepository; leave it nil when no fenced write surface is wired")
	}
	// applyDefaults must run before preflight so l.reconcilerID is the final value.
	l.applyDefaults()
	return l.metrics.preflight(l.reconcilerID)
}

// start performs the run setup (per-run state, worker pool, delaying queue,
// startup probe) after preStartValidate has passed. applyDefaults was already
// called inside preStartValidate, so all config fields hold their final values.
func (l *Loop) start(ownerCtx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil // already started — idempotent
	}
	l.gen++
	gen := l.gen

	runCtx, cancel := context.WithCancel(ownerCtx)

	ready := make(chan struct{})
	var readyOnce sync.Once
	var wg sync.WaitGroup

	if l.leader == nil {
		// Single-process mode: always-leader. Start the Trigger under runCtx (it
		// always holds "leadership"), then spawn the active work directly (the A3
		// behavior — Epoch 0, no fencing).
		if err := l.startTrigger(runCtx); err != nil {
			cancel()
			return err
		}
		l.spawnActive(runCtx, &wg, &readyOnce, ready)
	} else {
		// Leader-elect mode: a manager goroutine acquires/renews the lease and runs
		// the active work AND the Trigger under a per-term lease-scoped ctx — so a
		// FOLLOWER never starts the Trigger and never consumes its external source
		// before winning the lease (see startTrigger / runLeaseTerm).
		wg.Add(1)
		go l.leaderManage(runCtx, gen, &wg, &readyOnce, ready)
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
	//
	// In leader-elect mode the gauge is owned by leaderManage (set 1 on acquire,
	// 0 on loss/drain), so Start does NOT raise it here — this replica may be a
	// follower (gauge stays 0 until it wins the lease).
	if l.cancel != nil {
		if l.leader == nil {
			l.metrics.setLeader(runCtx, l.reconcilerID, 1)
		}
		l.logger.Info("reconcile: loop started",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
			slog.Bool("leader_elect", l.leader != nil))
	}
	return nil
}

// startTrigger starts the wired Trigger (if any) so it feeds Requests into
// triggerCh under ctx. It is leader-gated by its caller: single-process mode
// passes runCtx (always-leader); leader-elect mode's runLeaseTerm passes the
// lease-scoped ctx, so a FOLLOWER never starts the Trigger and thus never
// consumes its external source until it wins the lease (controller-runtime
// default: sources do not run before winning leader election; no warmup). No-op
// when no Trigger is wired.
//
// The Trigger spawns its own ctx-bound goroutine (not tracked by the Loop's
// WaitGroup, per the Trigger contract); it exits when ctx is canceled (lease
// loss / shutdown). Across lease terms the same triggerCh is reused — only one
// term is active at a time, so a brief overlap between a dying term's Trigger
// goroutine and the next term's is benign: channel sends are concurrent-safe and
// a stale buffered Request is a harmless idempotent re-reconcile.
func (l *Loop) startTrigger(ctx context.Context) error {
	if l.trigger == nil {
		return nil
	}
	if err := l.trigger.Start(ctx, l.triggerCh); err != nil {
		return fmt.Errorf("reconcile: trigger start failed: %w", err)
	}
	return nil
}

// spawnActive creates this term's work channels + per-term entity state and
// launches the worker pool, the optional Source feeder, and the shared delaying
// queue under ctx, all tracked by wg. It (re)initializes processing/dirty and a
// fresh backoff so each leadership term starts clean. In single-process mode it
// runs once under runCtx; in leader-elect mode runLeaseTerm calls it once per
// acquired lease term under a lease-scoped ctx (the previous term's goroutines
// are fully drained via leaseWG.Wait before re-init, so the map reset is race-free).
//
// readyOnce/readyCh signal "first worker live". In single-process mode this is
// Start's probe; in leader-elect mode leaderManage fires Start's probe itself and
// passes a throwaway per-term ready here.
func (l *Loop) spawnActive(ctx context.Context, wg *sync.WaitGroup, readyOnce *sync.Once, readyCh chan struct{}) {
	l.entityMu.Lock()
	l.processing = make(map[string]bool)
	l.dirty = make(map[string]Request)
	l.entityMu.Unlock()

	queue := make(chan Request, queueBuffer)
	addCh := make(chan waitingItem, addChBuffer)
	// cancelCh carries EntityIDs whose pending requeue must be removed from the
	// delaying queue (permanent dead-letter). Same buffer/lifecycle as addCh.
	cancelCh := make(chan string, addChBuffer)
	backoff := newEntityBackoff(l.baseDelay, l.maxDelay)

	workers := l.maxConcurrentReconciles
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readyOnce.Do(func() { close(readyCh) }) // first worker confirms the pool is live
			l.runWorker(ctx, queue, addCh, cancelCh, backoff)
		}()
	}
	if l.source != nil {
		wg.Add(1)
		go l.feedFromSource(ctx, queue, wg)
	}

	// F6: single shared delaying queue — ONE goroutine, ONE timer.
	// ref: kubernetes/client-go util/workqueue/delaying_queue.go
	wg.Add(1)
	go l.waitingLoop(ctx, addCh, cancelCh, queue, wg)
}

// leaderManage is the whole-loop leader-election driver (Leader != nil). It fires
// Start's readiness probe immediately — a follower has no workers, so Start must
// not block on one — then loops: acquire lease → run one lease term → repeat,
// until runCtx is canceled. Fail-closed: any AcquireLease error means "not the
// leader", so it does NOT dispatch and retries after leaderRetryPeriod.
//
// ref: kubernetes/client-go tools/leaderelection/leaderelection.go (acquire/renew
// loop). Deliberate divergence: client-go's manager log.Fatal()s the process on
// lost lease; a GoCell Loop is a cell lifecycle hook, not a standalone process,
// so on lost lease it cancels the lease-scoped ctx, drains the term, and
// re-contends as a follower without exiting.
func (l *Loop) leaderManage(runCtx context.Context, gen uint64, wg *sync.WaitGroup, readyOnce *sync.Once, readyCh chan struct{}) {
	defer wg.Done()
	readyOnce.Do(func() { close(readyCh) }) // Start's probe: confirmed once we begin contending

	for runCtx.Err() == nil {
		token, err := l.leader.AcquireLease(runCtx, l.reconcilerID)
		if err != nil {
			l.logLeaderAcquireSkip(runCtx, err)
			if !sleepCtx(runCtx, leaderRetryPeriod) {
				return
			}
			continue
		}
		l.runLeaseTerm(runCtx, gen, token)
	}
}

// runLeaseTerm holds leadership for one lease: it raises the leader gauge, runs
// the active work under a lease-scoped ctx, renews until the lease is lost or the
// loop shuts down, then drains the term and best-effort relinquishes. The term's
// goroutines are fully drained (leaseWG.Wait) before currentLease is cleared and
// before leaderManage can re-acquire, so the next term's spawnActive re-init is
// race-free. Generation-guarded so a superseded run's term cannot touch a newer
// run's gauge.
func (l *Loop) runLeaseTerm(runCtx context.Context, gen uint64, token LeaseToken) {
	l.currentLease.Store(&token)
	l.setLeaderGauge(gen, 1)

	leaseCtx, leaseCancel := context.WithCancel(runCtx)
	defer leaseCancel()

	var leaseWG sync.WaitGroup
	var termReadyOnce sync.Once
	termReady := make(chan struct{})
	// termReady is a throwaway per-term probe; Start's probe was already fired by leaderManage.
	l.spawnActive(leaseCtx, &leaseWG, &termReadyOnce, termReady)

	// Start the Trigger under the lease-scoped ctx: only the active holder consumes
	// the external source. On a start error, relinquish this term (skip renew, fall
	// through to teardown + release) so a healthy replica can take over.
	if err := l.startTrigger(leaseCtx); err != nil {
		l.logger.Warn("reconcile: trigger start failed; relinquishing lease term",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
			slog.Uint64("epoch", token.Epoch),
			slog.Any("error", redaction.RedactError(err)))
		leaseCancel()
	} else {
		l.renewLoop(leaseCtx, leaseCancel, token)
	}

	leaseCancel()  // interrupt in-flight Reconcile the instant the lease is gone
	leaseWG.Wait() // drain this term's workers / feeder / delaying queue
	l.currentLease.Store(nil)
	l.setLeaderGauge(gen, 0)
	l.releaseLease(runCtx, token)
}

// renewLoop renews the lease at the renew cadence until the lease is lost (it
// cancels the lease ctx the instant RenewLease fails, interrupting in-flight
// Reconcile per ADR §4.2) or leaseCtx is canceled (shutdown / outer cancel).
// Renewal keeps the LeaseToken.Epoch unchanged.
func (l *Loop) renewLoop(leaseCtx context.Context, leaseCancel context.CancelFunc, token LeaseToken) {
	ticker := controlPlaneClock{}.newRenewTicker(l.renewIntervalFor(token))
	defer ticker.Stop()
	for {
		select {
		case <-leaseCtx.Done():
			return
		case <-ticker.C:
			if err := l.leader.RenewLease(leaseCtx, token); err != nil {
				l.logLeaseLost(leaseCtx, token, err)
				leaseCancel()
				return
			}
		}
	}
}

// releaseLease best-effort relinquishes the lease so a follower takes over within
// ~1s instead of waiting for the elector's TTL. It uses a detached, bounded ctx
// (context.WithoutCancel + leaseReleaseTimeout) so that even on shutdown (runCtx
// already canceled) the release is attempted but a hung backend cannot stall
// Stop; on failure the lease simply expires on its own TTL (the fail-safe).
func (l *Loop) releaseLease(runCtx context.Context, token LeaseToken) {
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), leaseReleaseTimeout)
	defer cancel()
	if err := l.leader.ReleaseLease(relCtx, token); err != nil {
		l.logger.Warn("reconcile: lease release failed (will expire on TTL)",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
			slog.Uint64("epoch", token.Epoch),
			slog.Any("error", redaction.RedactError(err)))
	}
}

// setLeaderGauge writes reconcile_leader for this run, guarded by the generation
// check (a superseded leaderManage cannot clobber a newer run's gauge) — the same
// discipline as watchDrain. Uses context.Background() because the gauge is a
// point-in-time value and a 0-write may run after the run ctx is canceled.
func (l *Loop) setLeaderGauge(gen uint64, val float64) {
	l.mu.Lock()
	if l.gen == gen {
		l.metrics.setLeader(context.Background(), l.reconcilerID, val)
	}
	l.mu.Unlock()
}

// currentEpoch returns the live lease's monotonic fencing Epoch, or 0 when this
// replica holds no lease (single-process mode, or a follower between terms). The
// worker pool reads it to mint the per-Reconcile FencedWriter.
func (l *Loop) currentEpoch() uint64 {
	if t := l.currentLease.Load(); t != nil {
		return t.Epoch
	}
	return 0
}

// renewIntervalFor returns the lease renew cadence: the explicit RenewInterval
// override when set, else one-third of the token's TTL (ExpiresAt-AcquiredAt) so
// it adapts to the elector adapter's configured TTL, clamped to defaultRenewInterval
// when the token carries no usable TTL.
func (l *Loop) renewIntervalFor(token LeaseToken) time.Duration {
	if l.renewInterval > 0 {
		return l.renewInterval
	}
	if d := token.ExpiresAt.Sub(token.AcquiredAt) / renewIntervalDivisor; d > 0 {
		return d
	}
	return defaultRenewInterval
}

// sleepCtx sleeps for d or until ctx is canceled, returning false on cancel
// (caller should exit). Uses the sealed control-plane clock's real timer.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := controlPlaneClock{}.newRequeueTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// logLeaderAcquireSkip logs a failed/contended AcquireLease at Debug when it is a
// normal contention/shutdown signal (another holder owns the lease, or the loop's
// own ctx is canceled/deadline-exceeded) and Warn otherwise (backend I/O fault
// that an operator should see).
//
// ctx.Err() != nil is used instead of errors.Is(err, context.Canceled/DeadlineExceeded)
// so that only the Loop's OWN ctx cancellation is downgraded to Debug. A
// DeadlineExceeded that originates from an adapter-internal deadline while the
// loop ctx is still alive is a real I/O fault and should stay Warn.
func (l *Loop) logLeaderAcquireSkip(ctx context.Context, err error) {
	level := slog.LevelWarn
	if errors.Is(err, ErrLeaseHeld) || ctx.Err() != nil {
		// Contention (another holder owns the lease) or loop-shutdown (ctx canceled
		// or loop-owned deadline exceeded) are expected steady-state signals, not faults.
		level = slog.LevelDebug
	}
	l.logger.Log(ctx, level, "reconcile: leader-elect skip (lease not acquired)",
		slog.String("loop", l.name),
		slog.String("reconciler", l.reconcilerID),
		slog.Any("error", redaction.RedactError(err)))
}

// logLeaseLost logs a renewal failure at Warn. Two distinct cases:
//   - ErrReconcileLeaseLost: normal expected handoff (another holder took the
//     lease after our TTL expired); logs "lease lost; relinquishing leadership"
//     with the epoch so ops can correlate with the fencing audit trail.
//   - any other error: I/O fault (backend error, network issue); logs a distinct
//     message with io_fault=true so dashboards / alerts can route separately.
//
// Both paths cancel the lease ctx (via the caller), interrupting in-flight
// Reconcile per ADR §4.2.
func (l *Loop) logLeaseLost(ctx context.Context, token LeaseToken, err error) {
	if errors.Is(err, ErrReconcileLeaseLost) {
		l.logger.WarnContext(ctx, "reconcile: lease lost; relinquishing leadership",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
			slog.Uint64("epoch", token.Epoch),
			slog.Any("error", redaction.RedactError(err)))
	} else {
		l.logger.WarnContext(ctx, "reconcile: renew I/O error; abandoning lease term",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
			slog.Uint64("epoch", token.Epoch),
			slog.Bool("io_fault", true),
			slog.Any("error", redaction.RedactError(err)))
	}
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
		l.metrics.setLeader(context.Background(), l.reconcilerID, 0)
		l.cancel = nil
		l.done = nil
	}
	l.mu.Unlock()
	close(done)
}

// feedFromSource copies source into the internal queue until the run ctx is canceled.
func (l *Loop) feedFromSource(runCtx context.Context, queue chan<- Request, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-runCtx.Done():
			return
		case req, ok := <-l.source:
			if !ok {
				return // source closed — no more external feed
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
		l.metrics.recordResult(runCtx, l.reconcilerID, resultSkipped)
		return
	}
	l.processing[req.EntityID] = true
	l.entityMu.Unlock()

	l.metrics.inFlightDelta(runCtx, l.reconcilerID, 1)
	defer l.metrics.inFlightDelta(runCtx, l.reconcilerID, -1)

	// Inject the epoch-bound FencedWriter when a FencedRepository is wired: it is
	// the reconciler's only write surface (FencedWriterFrom). The epoch comes from
	// the live lease (currentEpoch()); single-process mode binds Epoch 0 (no
	// fencing). runCtx is the lease-scoped ctx in leader-elect mode, so a lost
	// lease cancels this Reconcile's ctx mid-write.
	//
	// Note: single-process mode binds Epoch 0 (always-accept, no fencing). A store
	// seeded from a prior leader-elect run (lastEpoch≥1) would reject epoch-0
	// writes. Do NOT point a single-process Loop at a store shared with a
	// leader-elect deployment — use distinct stores or a fresh store.
	reconcileCtx := runCtx
	if l.fencedRepo != nil {
		reconcileCtx = withFencedWriter(runCtx, newFencedWriter(l.fencedRepo, l.currentEpoch()))
	}

	start := controlPlaneClock{}.now()
	res, err := recoverReconcile(reconcileCtx, l.reconciler, req, l.logger, l.reconcilerID)
	l.metrics.observeDuration(runCtx, l.reconcilerID, controlPlaneClock{}.now().Sub(start).Seconds())

	label := classify(err)
	l.metrics.recordResult(runCtx, l.reconcilerID, label)

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
			delay = l.interval
		}
		l.enqueueDelayed(runCtx, req, delay, addCh)
	case resultPermanent:
		backoff.Forget(req.EntityID)
		// Cancel any pending requeue for this entity: an earlier success/transient
		// may have enqueued a (possibly long) interval/backoff item that, left in
		// place, would re-reconcile a now-dead-lettered entity once it fires. A
		// fresh source trigger re-observes it if the consumer resets its state.
		l.enqueueCancel(runCtx, req.EntityID, cancelCh)
		// ErrFencedWriteStale is an expected fencing race (this replica is no longer
		// the epoch owner); log at Warn, not Error, to avoid false-alarm alerting.
		// All other permanent errors are real dead-letters and warrant Error level.
		if errors.Is(err, ErrFencedWriteStale) {
			l.logger.Warn("reconcile: stale-epoch write rejected (fencing race); awaiting fresh trigger",
				slog.String("loop", l.name),
				slog.String("reconciler", l.reconcilerID),
				slog.String("entity", req.EntityID))
		} else {
			l.logger.Error("reconcile: permanent error (dead-letter; not requeued)",
				slog.String("loop", l.name),
				slog.String("reconciler", l.reconcilerID),
				slog.String("entity", req.EntityID),
				slog.Any("error", redaction.RedactError(err)))
		}
	default: // resultTransient (including recovered panics)
		delay := backoff.When(req.EntityID)
		l.logger.Warn("reconcile: transient error (requeued with backoff)",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID),
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
		l.logger.Warn("reconcile: owner ctx canceled before loop confirmed running",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID))
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
		l.logger.Info("reconcile: loop stopped",
			slog.String("loop", l.name),
			slog.String("reconciler", l.reconcilerID))
		return nil
	case <-ctx.Done():
		// Budget exhausted before drain. cancel() has fired so the goroutines
		// are unwinding; leave l.cancel/l.done intact so Stop is retryable and
		// Start cannot race the draining pool.
		return ctx.Err()
	}
}

// applyDefaults fills all zero-valued config fields with their defaults. It is
// called once at the top of preStartValidate() so every subsequent field read
// sees the final value. This is the single defaulting funnel — there are no
// lazy getter methods (controller-runtime doController() eager-default pattern).
func (l *Loop) applyDefaults() {
	if l.interval <= 0 {
		l.interval = defaultReconcileInterval
	}
	if l.maxConcurrentReconciles <= 0 {
		l.maxConcurrentReconciles = defaultMaxConcurrentReconciles
	}
	if l.name == "" {
		l.name = defaultLoopName
	}
	if l.reconcilerID == "" {
		l.reconcilerID = reconcilerIDSentinel
	}
	if l.logger == nil {
		l.logger = slog.Default()
	}
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
