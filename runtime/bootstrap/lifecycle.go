package bootstrap

// ref: uber-go/fx internal/lifecycle/lifecycle.go — Hook, numStarted LIFO rollback.
// Adopted: numStarted counter, LIFO Stop, Stop is best-effort, Start is fail-fast.
// Deviated: per-hook StartTimeout/StopTimeout (fx uses shared); Append-after-Start
//           returns ErrLifecycleAlreadyStarted (fx silent); errors.Join replaces
//           multierr.Combine (kernel zero-dep principle).

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrLifecycleAlreadyStarted is returned by Append when the lifecycle has
// already started, and by Start when Start is called more than once.
var ErrLifecycleAlreadyStarted = errors.New("bootstrap: lifecycle already started")

const (
	// DefaultStartTimeout is the default per-hook start deadline.
	DefaultStartTimeout = 30 * time.Second
	// DefaultStopTimeout is the default per-hook stop deadline.
	DefaultStopTimeout = 10 * time.Second
)

// Hook is a pair of lifecycle callbacks invoked in Append order on Start and
// reverse order on Stop. A zero-value OnStart/OnStop means no-op.
type Hook struct {
	Name         string                          // diagnostic name (log dimension)
	OnStart      func(ctx context.Context) error // nil = no-op
	OnStop       func(ctx context.Context) error // nil = no-op
	StartTimeout time.Duration                   // 0=use default, <0=no timeout
	StopTimeout  time.Duration                   // 0=use default, <0=no timeout
}

// Lifecycle manages an ordered Hook sequence.
//
// Five-state machine: stopped → starting → (incompleteStart | started) → stopping → stopped
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
	numStarted   int // number of hooks whose OnStart succeeded
	defaultStart time.Duration
	defaultStop  time.Duration
	logger       *slog.Logger
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
	return &lifecycle{
		state:        stateStopped,
		defaultStart: defaultStart,
		defaultStop:  defaultStop,
		logger:       logger,
	}
}

// Append registers a Hook. Returns ErrLifecycleAlreadyStarted if the
// lifecycle is not in stopped state.
func (lc *lifecycle) Append(h Hook) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	switch lc.state {
	case stateStarting, stateStarted, stateStopping, stateIncompleteStart:
		return ErrLifecycleAlreadyStarted
	}
	lc.hooks = append(lc.hooks, h)
	return nil
}

// Start executes OnStart for each hook in Append order (fail-fast).
// On failure, already-started hooks are rolled back in LIFO order.
// The failed hook's OnStop is NOT called.
func (lc *lifecycle) Start(ctx context.Context) error { //nolint:cyclop // state machine switch is inherently branchy
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

	lc.logger.InfoContext(ctx, startMsg, slog.String("name", h.Name))

	hookCtx, cancel := lc.applyTimeout(ctx, hookTimeout)
	defer cancel()

	t0 := time.Now()
	err := fn(hookCtx)
	elapsed := time.Since(t0)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			lc.logger.ErrorContext(ctx, "hook.timeout",
				slog.String("name", h.Name),
				slog.String("phase", startMsg),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err))
		} else {
			lc.logger.ErrorContext(ctx, errMsg,
				slog.String("name", h.Name),
				slog.Duration("elapsed", elapsed),
				slog.Any("error", err))
		}
		return err
	}

	lc.logger.InfoContext(ctx, okMsg,
		slog.String("name", h.Name),
		slog.Duration("elapsed", elapsed))
	return nil
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
