package bootstrap

// ref: uber-go/fx internal/lifecycle/lifecycle.go — Hook, numStarted LIFO rollback.
// Adopted: numStarted counter, LIFO Stop, Stop is best-effort, Start is fail-fast.
// Deviated: per-hook StartTimeout/StopTimeout (fx uses shared); Append-after-Start
//           returns ErrLifecycleAlreadyStarted (fx silent); errors.Join replaces
//           multierr.Combine (kernel zero-dep principle).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ErrLifecycleAlreadyStarted is returned by Append when the lifecycle has
// already started, and by Start when Start is called more than once.
//
// Callers may use errors.Is(err, ErrLifecycleAlreadyStarted) for sentinel
// checks; the pointer identity is stable for the lifetime of the process.
var ErrLifecycleAlreadyStarted = errcode.New(errcode.KindInternal, errcode.ErrBootstrapLifecycle, "lifecycle already started")

// ErrDuplicateHookName is returned by Append when a non-empty Hook.Name has
// already been registered. The single source of truth for duplicate-name
// detection lives here so that phase3b (cell.Registry.Lifecycle snapshot drain)
// and WithLifecycle (explicit composition-root Append) share the same guard
// without having to re-synchronize per-path "seen" maps.
//
// Empty Name bypasses the check — callers that deliberately register nameless
// hooks accept the risk of duplicates (diagnostic cost, not correctness).
var ErrDuplicateHookName = errcode.New(errcode.KindInternal, errcode.ErrBootstrapLifecycle, "duplicate lifecycle hook name")

// ErrBootstrapStartupTimeout is returned by Bootstrap.Run when lifecycle.Start
// does not complete within the orchestration-layer startup budget
// (WithStartupTimeout, default DefaultStartupTimeout). It is the deadlock
// backstop for a hook whose OnStart never returns (ADR 202605170000 §D-B
// amendment / review P1-1): caller-ctx cancellation is the primary abort, this
// sentinel covers "caller never cancels and an OnStart wedged".
//
// Reuses ErrBootstrapLifecycle so no new errcode sentinel/Kind is introduced
// (contract-fanout not triggered). Callers may errors.Is against it.
var ErrBootstrapStartupTimeout = errcode.New(errcode.KindInternal, errcode.ErrBootstrapLifecycle, "lifecycle startup exceeded budget")

const (
	// DefaultStartTimeout is the default per-hook StartTimeout. Since ADR
	// 202605170000 §D-B it is NOT an enforced OnStart ctx deadline — it is the
	// slow-start warning threshold only (warn when OnStart elapsed ≥ 80% of
	// it). The whole-Start deadlock backstop is the orchestration-layer
	// supervision in Bootstrap.Run (caller ctx + WithStartupTimeout), not this.
	DefaultStartTimeout = 30 * time.Second
	// DefaultStopTimeout is the default per-hook stop deadline.
	DefaultStopTimeout = 10 * time.Second

	// DefaultStartupTimeout is the default whole-Start orchestration budget
	// (Bootstrap.Run supervises lifecycle.Start with caller ctx + this timer).
	// It is a deadlock backstop, NOT a per-hook SLA: well-behaved hooks
	// fast-probe-return far inside it. Override with WithStartupTimeout;
	// negative disables the timer (caller-ctx-only abort).
	DefaultStartupTimeout = 30 * time.Second

	// hookSlowNumerator and hookSlowDenominator define the slow-start warning
	// threshold as a fraction of the hook timeout: threshold = timeout * num / den.
	// 8/10 = 80% of the timeout is the slow-start warning boundary.
	hookSlowNumerator   = 8
	hookSlowDenominator = 10
)

// Hook is a pair of lifecycle callbacks invoked in Append order on Start and
// reverse order on Stop. A zero-value OnStart/OnStop means no-op.
//
// The CellID field is stamped by phase3b when a hook is drained from a
// cell's Registry.Lifecycle snapshot; hooks appended via bootstrap.WithLifecycle
// leave it empty. It is a runtime-only observability dimension (not mirrored
// on cell.LifecycleHook) — cells never self-declare their identity here, by
// analogy with uber-go/fx's unexported Hook.callerFrame which the framework
// fills in at Append time rather than trusting the caller to pass it.
//
// ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go (callerFrame)
// ref: kubernetes/kubernetes pkg/kubelet/lifecycle/handlers.go
//
//	(containerName + pod structured slog fields, not name-encoded)
type Hook struct {
	CellID string // runtime-stamped by phase3b; "" for WithLifecycle-appended hooks
	Name   string // diagnostic name (log dimension)

	// OnStart is called with the owner ctx (long-lived, not a startup-deadline
	// ctx). Bootstrap passes the ownerCtx derived from runCtx directly, without
	// wrapping in a StartTimeout deadline. StartTimeout is retained as the hook's
	// self-declared probe-window budget (informational; runner does not enforce).
	//
	// Supersedes ADR 202605102000 §D1 ("OnStart ctx carries startup-deadline
	// semantics"). The new contract aligns with controller-runtime
	// Runnable.Start(managerCtx): the hook should spawn a long-running goroutine
	// on ownerCtx, run a fast synchronous probe, and return.
	//
	// OnStart hooks run sequentially in the Start goroutine; a slow or blocking
	// OnStart delays all subsequent hooks, and the aggregate elapsed time may
	// exceed WithStartupTimeout — later hooks then never get a chance to start.
	// Implementations MUST spawn their worker goroutine and fast-probe-return,
	// respecting ctx for cooperative cancellation.
	//
	// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go (engageStopProcedure)
	OnStart func(ctx context.Context) error // nil = no-op

	// OnStop carries a context with StopTimeout deadline applied by the runner.
	OnStop func(ctx context.Context) error // nil = no-op
	// StartTimeout is the hook's self-declared probe budget. Informational only —
	// the runner does NOT enforce it as an OnStart ctx deadline (see ADR
	// 202605102000 §D1 RETRACTED). Used only for slow-start warning threshold.
	// Previously the runner applied context.WithTimeout(StartTimeout) to OnStart;
	// that behavior was retracted when OnStart ctx was redefined as owner ctx
	// (superseded by ADR 202605170000 §D-B).
	StartTimeout time.Duration
	StopTimeout  time.Duration // 0=use default, <0=no timeout
}

// Lifecycle manages an ordered Hook sequence.
//
// Five-state machine: stopped → starting → (incompleteStart | started) → stopping → stopped.
type Lifecycle interface {
	Append(h Hook) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// LifecycleConfig configures a Lifecycle instance.
type LifecycleConfig struct {
	DefaultStartTimeout time.Duration // 0 → DefaultStartTimeout constant
	DefaultStopTimeout  time.Duration // 0 → DefaultStopTimeout constant
	Logger              *slog.Logger  // nil → slog.Default()
	Clock               clock.Clock   // required: NewLifecycle panics when nil
}

// lifecycleState represents the current state of the lifecycle state machine.
type lifecycleState int

const (
	stateStopped         lifecycleState = iota
	stateStarting        lifecycleState = iota
	stateStarted         lifecycleState = iota
	stateIncompleteStart lifecycleState = iota
	stateStopping        lifecycleState = iota
)

// lifecycle is the concrete implementation of Lifecycle.
type lifecycle struct {
	mu           sync.Mutex
	state        lifecycleState
	hooks        []Hook
	names        map[string]struct{} // tracks non-empty Hook.Name for dup detection
	numStarted   int                 // number of hooks whose OnStart succeeded
	defaultStart time.Duration
	defaultStop  time.Duration
	logger       *slog.Logger
	clock        clock.Clock

	// workCtx is the OnStart context handed to every hook. It is a child of
	// the ctx passed to Start (the bootstrap ownerCtx). workCancel is invoked
	// BEFORE the LIFO rollback on a partial Start failure so the failed hook's
	// own already-spawned goroutine (bound to workCtx) is torn down before any
	// successfully-started hook's OnStop runs — closing the C.2 gap where a
	// failed hook's worker kept running during sibling rollback (review P1-2).
	//
	// OnStop is NOT derived from workCtx: rollback / Stop pass the original
	// Start ctx so OnStop still has a live context to drain in-flight work
	// after workCancel has fired.
	//
	// Single-ctx-truth (ADR 202605170000 §D-B) is preserved: a hook still sees
	// exactly one context whose only cancellation triggers are ownerCancel
	// (assembly shutdown) and start-failure teardown — both "owner is going
	// away", never a competing startup deadline.
	//
	// Concurrency invariant: workCtx / workCancel are written exactly once under
	// mu at the top of Start (before the single-goroutine hook loop). After that
	// point workCtx is read-only and is accessed only from the Start goroutine
	// (via hookContext). No further locking is required for reads. OnStop must
	// NOT assume a spawned goroutine is still alive because workCancel may have
	// already fired; use a buffered done channel for goroutine rendezvous.
	workCtx    context.Context
	workCancel context.CancelFunc
}

// NewLifecycle creates a new Lifecycle with the given config.
func NewLifecycle(cfg LifecycleConfig) Lifecycle {
	defaultStart := cfg.DefaultStartTimeout
	if defaultStart == 0 {
		defaultStart = DefaultStartTimeout
	}
	defaultStop := cfg.DefaultStopTimeout
	if defaultStop == 0 {
		defaultStop = DefaultStopTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock.MustHaveClock(cfg.Clock, "bootstrap.NewLifecycle")
	clk := cfg.Clock
	return &lifecycle{
		state:        stateStopped,
		names:        make(map[string]struct{}),
		defaultStart: defaultStart,
		defaultStop:  defaultStop,
		logger:       logger,
		clock:        clk,
	}
}

// Append registers a Hook. Returns ErrLifecycleAlreadyStarted if the
// lifecycle is not in stopped state, or ErrDuplicateHookName when h.Name
// is non-empty and already registered. Empty Name is allowed through so
// callers who deliberately register nameless hooks accept the diagnostic
// cost of duplicates.
func (lc *lifecycle) Append(h Hook) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	switch lc.state {
	case stateStarting, stateStarted, stateStopping, stateIncompleteStart:
		return ErrLifecycleAlreadyStarted
	}
	if h.Name != "" {
		if _, dup := lc.names[h.Name]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateHookName, h.Name)
		}
		lc.names[h.Name] = struct{}{}
	}
	lc.hooks = append(lc.hooks, h)
	return nil
}

// Start executes OnStart for each hook in Append order (fail-fast).
// On failure, already-started hooks are rolled back in LIFO order.
// The failed hook's OnStop is NOT called.
func (lc *lifecycle) Start(ctx context.Context) error {
	lc.mu.Lock()
	if lc.state != stateStopped {
		lc.mu.Unlock()
		return ErrLifecycleAlreadyStarted
	}
	lc.state = stateStarting
	// workCtx is the single OnStart context for every hook (child of the
	// bootstrap ownerCtx). Canceling it on partial-start failure tears down
	// the failed hook's own spawned goroutine before sibling rollback (P1-2).
	lc.workCtx, lc.workCancel = context.WithCancel(ctx)
	lc.mu.Unlock()

	for i, h := range lc.hooks {
		if err := lc.runHook(ctx, h, true); err != nil {
			// Start failed: cancel workCtx FIRST so the failed hook's own
			// already-spawned goroutine (and every started hook's worker)
			// observes cancellation before LIFO OnStop runs (P1-2). OnStop
			// itself uses the original ctx (not workCtx) so it still has a
			// live context to drain.
			lc.workCancel()

			// Roll back all hooks that succeeded (indices 0..i-1) in LIFO.
			lc.mu.Lock()
			lc.state = stateIncompleteStart
			lc.mu.Unlock()

			rollbackErrs := lc.rollback(ctx)
			return errors.Join(append([]error{err}, rollbackErrs...)...)
		}
		lc.mu.Lock()
		lc.numStarted = i + 1
		lc.mu.Unlock()
	}

	lc.mu.Lock()
	lc.state = stateStarted
	lc.mu.Unlock()
	return nil
}

// Stop executes OnStop for all successfully-started hooks in LIFO order.
// All OnStop functions are called regardless of individual errors (best-effort).
// Returns errors.Join of all OnStop errors.
// Stop is idempotent: calling it when already stopped or stopping returns nil.
// Stop is also safe to call after a partial Start failure (the internal
// incomplete-start state); it completes the rollback of already-started hooks.
func (lc *lifecycle) Stop(ctx context.Context) error {
	lc.mu.Lock()
	switch lc.state {
	case stateStopped, stateStopping:
		lc.mu.Unlock()
		return nil
	}
	lc.state = stateStopping
	lc.mu.Unlock()

	errs := lc.rollback(ctx)

	// Release workCtx after OnStop has drained (OnStop used the original ctx,
	// not workCtx, so canceling here does not truncate any in-flight drain).
	// Direct lifecycle.Stop callers (tests) rely on this to avoid a leaked
	// context; in the bootstrap path ownerCancel already cascaded.
	lc.mu.Lock()
	if lc.workCancel != nil {
		lc.workCancel()
	}
	lc.state = stateStopped
	lc.mu.Unlock()

	return errors.Join(errs...)
}

// rollback runs OnStop for all hooks whose OnStart succeeded, in LIFO order.
// It returns a slice of all errors encountered (best-effort: all hooks are tried).
// The failed hook's OnStop is never in numStarted, so it is never called.
func (lc *lifecycle) rollback(ctx context.Context) []error {
	lc.mu.Lock()
	numStarted := lc.numStarted
	hooks := lc.hooks
	lc.mu.Unlock()

	var errs []error
	for ; numStarted > 0; numStarted-- {
		h := hooks[numStarted-1]
		if err := lc.runHook(ctx, h, false); err != nil {
			errs = append(errs, err)
		}
	}

	lc.mu.Lock()
	lc.numStarted = 0
	lc.mu.Unlock()

	return errs
}

// hookPhase holds the resolved metadata for a single runHook invocation.
type hookPhase struct {
	fn       func(context.Context) error
	timeout  time.Duration
	startMsg string
	okMsg    string
	errMsg   string
	isStart  bool
}

// resolveHookPhase extracts fn, timeout, and log-message labels from h for
// the start or stop direction. Extracted to keep runHook under the ≤15
// cognitive-complexity limit.
func (lc *lifecycle) resolveHookPhase(h Hook, isStart bool) hookPhase {
	if isStart {
		t := h.StartTimeout
		if t == 0 {
			t = lc.defaultStart
		}
		return hookPhase{
			fn:       h.OnStart,
			timeout:  t,
			startMsg: "hook.start",
			okMsg:    "hook.start_ok",
			errMsg:   "hook.start_err",
			isStart:  true,
		}
	}
	t := h.StopTimeout
	if t == 0 {
		t = lc.defaultStop
	}
	return hookPhase{
		fn:       h.OnStop,
		timeout:  t,
		startMsg: "hook.stop",
		okMsg:    "hook.stop_ok",
		errMsg:   "hook.stop_err",
		isStart:  false,
	}
}

// runHook executes either OnStart (isStart=true) or OnStop (isStart=false) for h.
//
// OnStart path (isStart=true): ctx is passed directly to fn — it is the long-lived
// owner ctx, NOT wrapped in a StartTimeout deadline. StartTimeout is used only for
// the slow-start warning threshold (informational). This supersedes ADR 202605102000
// §D1, which wrapped OnStart in applyTimeout(StartTimeout).
//
// OnStop path (isStart=false): applyTimeout(StopTimeout) is applied as before.
// Logs before/after each call.
func (lc *lifecycle) runHook(ctx context.Context, h Hook, isStart bool) error {
	p := lc.resolveHookPhase(h, isStart)
	if p.fn == nil {
		return nil
	}

	lc.logger.LogAttrs(ctx, slog.LevelInfo, p.startMsg, hookIdentityAttrs(h)...)

	hookCtx, cancel := lc.hookContext(ctx, p)
	defer cancel()

	t0 := lc.clock.Now()
	err := p.fn(hookCtx)
	elapsed := lc.clock.Since(t0)

	if err != nil {
		lc.logHookError(ctx, h, p, elapsed, err)
		return err
	}

	lc.logHookOK(ctx, h, p, elapsed)
	return nil
}

// hookContext returns the context and cancel func for the hook invocation.
// OnStart receives the owner ctx directly (no timeout wrapping); StartTimeout
// is retained as a slow-start warning threshold only.
// OnStop applies applyTimeout(StopTimeout).
//
// workCtx read is concurrency-safe here: it is written exactly once under mu
// before the hook loop in Start; this method is only ever called from the
// Start goroutine (the sequential hook loop), so no additional locking is
// needed. OnStop hooks must NOT assume a goroutine spawned in OnStart is still
// alive when OnStop runs — workCancel may have fired; use a buffered done
// channel for goroutine rendezvous in OnStop.
func (lc *lifecycle) hookContext(ctx context.Context, p hookPhase) (context.Context, context.CancelFunc) {
	if p.isStart {
		// OnStart receives workCtx (child of the Start ctx = ownerCtx). It is
		// the single owner-lifetime ctx (ADR 202605170000 §D-B); the only
		// extra cancellation trigger vs ownerCtx is start-failure teardown.
		return lc.workCtx, func() {
			// Intentional no-op, not an incomplete implementation:
			// workCtx's lifetime is owned by Start/Stop via workCancel.
			// runHook defers this CancelFunc; if it canceled workCtx, the
			// first OnStart's deferred cancel would tear down the shared
			// owner ctx before later hooks (and their workers) even start.
		}
	}
	return lc.applyTimeout(ctx, p.timeout)
}

// logHookError emits the appropriate error log for a failed hook.
func (lc *lifecycle) logHookError(ctx context.Context, h Hook, p hookPhase, elapsed time.Duration, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		attrs := append(hookIdentityAttrs(h),
			slog.String("phase", p.startMsg),
			slog.Duration("elapsed", elapsed),
			slog.Any("error", err))
		lc.logger.LogAttrs(ctx, slog.LevelError, "hook.timeout", attrs...)
	} else {
		attrs := append(hookIdentityAttrs(h),
			slog.Duration("elapsed", elapsed),
			slog.Any("error", err))
		lc.logger.LogAttrs(ctx, slog.LevelError, p.errMsg, attrs...)
	}
}

// logHookOK emits the ok log and optionally the slow-start warning.
func (lc *lifecycle) logHookOK(ctx context.Context, h Hook, p hookPhase, elapsed time.Duration) {
	if p.isStart && p.timeout > 0 {
		threshold := p.timeout * hookSlowNumerator / hookSlowDenominator
		if elapsed >= threshold {
			attrs := append(hookIdentityAttrs(h),
				slog.Duration("elapsed", elapsed),
				slog.Duration("timeout", p.timeout),
				slog.Duration("threshold", threshold))
			lc.logger.LogAttrs(ctx, slog.LevelWarn, "hook.start_slow", attrs...)
		}
	}
	okAttrs := append(hookIdentityAttrs(h), slog.Duration("elapsed", elapsed))
	lc.logger.LogAttrs(ctx, slog.LevelInfo, p.okMsg, okAttrs...)
}

// hookIdentityAttrs returns the identity slog.Attrs for h: always "name",
// and "cell" when CellID is non-empty. Empty CellID indicates a hook
// registered via bootstrap.WithLifecycle rather than phase3b auto-discovery;
// omitting the field keeps log lines clean rather than emitting cell="".
func hookIdentityAttrs(h Hook) []slog.Attr {
	if h.CellID == "" {
		return []slog.Attr{slog.String("name", h.Name)}
	}
	return []slog.Attr{slog.String("name", h.Name), slog.String("cell", h.CellID)}
}

// applyTimeout derives a child context with deadline from parentCtx.
// If d < 0, no deadline is applied and the parentCtx is returned as-is.
// The returned cancel func must always be called by the caller.
func (lc *lifecycle) applyTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d < 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
