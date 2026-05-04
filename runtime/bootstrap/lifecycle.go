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

const (
	// DefaultStartTimeout is the default per-hook start deadline.
	DefaultStartTimeout = 30 * time.Second
	// DefaultStopTimeout is the default per-hook stop deadline.
	DefaultStopTimeout = 10 * time.Second

	// hookSlowNumerator and hookSlowDenominator define the slow-start warning
	// threshold as a fraction of the hook timeout: threshold = timeout * num / den.
	// 8/10 = 80% of the timeout is the slow-start warning boundary.
	hookSlowNumerator   time.Duration = 8
	hookSlowDenominator time.Duration = 10
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
	CellID       string                          // runtime-stamped by phase3b; "" for WithLifecycle-appended hooks
	Name         string                          // diagnostic name (log dimension)
	OnStart      func(ctx context.Context) error // nil = no-op
	OnStop       func(ctx context.Context) error // nil = no-op
	StartTimeout time.Duration                   // 0=use default, <0=no timeout
	StopTimeout  time.Duration                   // 0=use default, <0=no timeout
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
	lc.mu.Unlock()

	for i, h := range lc.hooks {
		if err := lc.runHook(ctx, h, true); err != nil {
			// Start failed: roll back all hooks that succeeded (indices 0..i-1) in LIFO.
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

	lc.mu.Lock()
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

// runHook executes either OnStart (isStart=true) or OnStop (isStart=false) for h,
// applying the per-hook timeout. Logs before/after each call.
func (lc *lifecycle) runHook(ctx context.Context, h Hook, isStart bool) error {
	var fn func(context.Context) error
	var hookTimeout time.Duration
	var startMsg, okMsg, errMsg string

	if isStart {
		fn = h.OnStart
		hookTimeout = h.StartTimeout
		if hookTimeout == 0 {
			hookTimeout = lc.defaultStart
		}
		startMsg = "hook.start"
		okMsg = "hook.start_ok"
		errMsg = "hook.start_err"
	} else {
		fn = h.OnStop
		hookTimeout = h.StopTimeout
		if hookTimeout == 0 {
			hookTimeout = lc.defaultStop
		}
		startMsg = "hook.stop"
		okMsg = "hook.stop_ok"
		errMsg = "hook.stop_err"
	}

	if fn == nil {
		return nil
	}

	lc.logger.LogAttrs(ctx, slog.LevelInfo, startMsg, hookIdentityAttrs(h)...)

	hookCtx, cancel := lc.applyTimeout(ctx, hookTimeout)
	defer cancel()

	t0 := lc.clock.Now()
	err := fn(hookCtx)
	elapsed := lc.clock.Since(t0)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			attrs := append(hookIdentityAttrs(h),
				slog.String("phase", startMsg),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err))
			lc.logger.LogAttrs(ctx, slog.LevelError, "hook.timeout", attrs...)
		} else {
			attrs := append(hookIdentityAttrs(h),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err))
			lc.logger.LogAttrs(ctx, slog.LevelError, errMsg, attrs...)
		}
		return err
	}

	if isStart && hookTimeout > 0 {
		threshold := hookTimeout * hookSlowNumerator / hookSlowDenominator
		if elapsed >= threshold {
			attrs := append(hookIdentityAttrs(h),
				slog.Duration("elapsed", elapsed),
				slog.Duration("timeout", hookTimeout),
				slog.Duration("threshold", threshold))
			lc.logger.LogAttrs(ctx, slog.LevelWarn, "hook.start_slow", attrs...)
		}
	}

	okAttrs := append(hookIdentityAttrs(h), slog.Duration("elapsed", elapsed))
	lc.logger.LogAttrs(ctx, slog.LevelInfo, okMsg, okAttrs...)
	return nil
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
