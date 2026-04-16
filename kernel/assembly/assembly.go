// Package assembly provides the CoreAssembly that orchestrates Cell
// lifecycle (register, start, stop, health).
//
// Design ref: uber-go/fx app.go, lifecycle.go
//   - FIFO Start / LIFO Stop
//   - Start 失败自动 rollback 已启动的 Cell
//   - Stop 尽力而为，合并错误
//   - 状态机防止重入
//
// ref: go-kratos/kratos app.go — BeforeStart/AfterStart/BeforeStop/AfterStop
// ref: uber-go/fx lifecycle.go — FIFO Start, LIFO Stop, rollback on failure
package assembly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// assemblyState represents the lifecycle state of a CoreAssembly.
// ref: uber-go/fx lifecycle.go — stopped/starting/started/stopping
type assemblyState int

const (
	stateStopped  assemblyState = iota
	stateStarting               // Start() 正在执行
	stateStarted                // Start() 成功完成
	stateStopping               // Stop() 正在执行
)

// DefaultHookTimeout is the per-hook deadline applied when Config.HookTimeout
// is zero. 30s accommodates slow-starting cells (DB pool warm-up, readiness
// probes) while still bounding a stuck hook.
//
// ref: uber-go/fx app.go@master:L54 DefaultTimeout=15s (assembly-wide).
// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go@main:L394-L399
// GracefulShutdownTimeout default (per-phase, user-configured).
// GoCell picks 30s as midpoint — cell lifecycle is closer to k8s scope.
const DefaultHookTimeout = 30 * time.Second

// Config holds assembly-level configuration.
type Config struct {
	ID             string
	DurabilityMode cell.DurabilityMode // Required: Demo or Durable (zero value rejected by CheckNotNoop)

	// HookTimeout bounds every BeforeStart/AfterStart/BeforeStop/AfterStop
	// hook invocation. Zero uses DefaultHookTimeout. Set to a negative value
	// to disable per-hook timeouts entirely (hook inherits parent ctx only).
	//
	// Semantics: soft-cancel only — when the deadline fires the assembly
	// records OutcomeTimeout and returns context.DeadlineExceeded to the
	// caller, but the hook goroutine continues until it observes ctx (GoCell
	// hooks are synchronous and internal, so a well-behaved hook respects
	// ctx; a misbehaving hook is detected via the timeout event).
	HookTimeout time.Duration

	// HookObserver receives one HookEvent per invocation of every lifecycle
	// hook. Nil uses cell.NopHookObserver{} (allocation-free no-op).
	// Implementations must not block the caller.
	HookObserver cell.LifecycleHookObserver

	// HookObserverQueueSize bounds the async dispatcher's pending-event
	// buffer. Zero uses DefaultHookObserverQueueSize (128). Negative is
	// clamped to default.
	HookObserverQueueSize int

	// HookObserverSinkTimeout bounds a single OnHookEvent invocation. When
	// exceeded, the event is counted as dropped with reason="sink_timeout"
	// and the dispatcher moves on (the observer goroutine is abandoned).
	// Zero uses DefaultHookObserverSinkTimeout (5s).
	HookObserverSinkTimeout time.Duration

	// HookObserverDrainTimeout bounds Stop()'s wait for the dispatcher to
	// drain remaining events after the channel is closed. Zero uses
	// DefaultHookObserverDrainTimeout (5s). After drain completion or
	// timeout, any further emit() calls are counted as queue_full.
	HookObserverDrainTimeout time.Duration

	// MetricsProvider receives the dispatcher's internal drop / queue-depth
	// metrics. Nil uses metrics.NopProvider, preserving the prior
	// zero-dependency default. Wire a real provider to make dropped events
	// visible in dashboards.
	MetricsProvider metrics.Provider
}

// CoreAssembly is the default Assembly implementation. It manages a set of
// Cells, starting them in registration order and stopping them in reverse.
type CoreAssembly struct {
	mu         sync.Mutex
	id         string
	cfg        Config
	cells      []cell.Cell
	cellMap    map[string]cell.Cell
	state      assemblyState
	dispatcher *hookDispatcher // owned; lifecycle tied to New/Stop
}

// New creates a CoreAssembly with the given configuration.
//
// If cfg.HookObserver is nil, cell.NopHookObserver{} is substituted so the
// hook call sites can emit unconditionally.
// If cfg.HookTimeout is zero, DefaultHookTimeout is applied. Negative value
// disables per-hook timeout entirely.
func New(cfg Config) *CoreAssembly {
	// Normalise nil + typed-nil (interface wrapping a nil pointer) to
	// NopHookObserver. A typed nil that slips through would dispatch to a
	// nil receiver on every hook and only manifest as panic-recover log
	// spam from emitHookEvent.
	if cell.IsNilHookObserver(cfg.HookObserver) {
		cfg.HookObserver = cell.NopHookObserver{}
	}
	if cfg.HookTimeout == 0 {
		cfg.HookTimeout = DefaultHookTimeout
	}
	// Eagerly construct the async dispatcher at New() time (not lazy on first
	// emit) so its lifetime is deterministic: callers that construct an
	// assembly and never Start it can still call Stop to drain cleanly, and
	// goleak-based tests cannot witness a racy lazy-start.
	dispatcher, err := newHookDispatcher(cfg.HookObserver, cfg.HookObserverQueueSize, cfg.HookObserverSinkTimeout, cfg.MetricsProvider)
	if err != nil {
		// newHookDispatcher only fails if metrics registration fails, and
		// even then it falls back to Nop internally — so this branch is
		// defensive. Logging + continuing with a fresh Nop dispatcher keeps
		// New() infallible from callers' perspective.
		slog.Warn("assembly: hook dispatcher construction failed; continuing without async fan-out",
			slog.Any("error", err))
	}
	return &CoreAssembly{
		id:         cfg.ID,
		cfg:        cfg,
		cellMap:    make(map[string]cell.Cell),
		dispatcher: dispatcher,
	}
}

// Register adds a Cell to the assembly. It returns an error if the Cell ID is
// empty, already registered, or the assembly has already been started.
func (a *CoreAssembly) Register(c cell.Cell) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state != stateStopped {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot register in state %d", a.id, a.state))
	}

	if c == nil {
		return errcode.New(errcode.ErrValidationFailed, "cell must not be nil")
	}

	id := c.ID()
	if id == "" {
		return errcode.New(errcode.ErrValidationFailed, "cell ID must not be empty")
	}
	if _, exists := a.cellMap[id]; exists {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("duplicate cell ID: %q", id))
	}
	a.cells = append(a.cells, c)
	a.cellMap[id] = c
	return nil
}

// Start initialises and starts every registered Cell in registration order.
// Dependencies are built from all registered Cells.
//
// ref: uber-go/fx app.go — Start 出错后自动 rollback 已启动的 Cell（LIFO Stop）。
func (a *CoreAssembly) Start(ctx context.Context) error {
	return a.startInternal(ctx, nil)
}

// Stop stops every registered Cell in reverse registration order. If multiple
// Cells fail, Stop continues and returns all errors joined via errors.Join.
// Stop is only allowed from the Started state; calling Stop in any other state
// is a no-op.
//
// For each cell, the sequence is: BeforeStop → Stop → AfterStop.
// All three phases are best-effort — errors are accumulated, never abort.
//
// ref: uber-go/fx app.go — Stop 尽力而为，不因某个 hook 失败而中止。
// ref: go-kratos/kratos app.go — BeforeStop/AfterStop hooks around server.Stop
func (a *CoreAssembly) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.state != stateStarted {
		a.mu.Unlock()
		return nil // Only allow Stop from Started state.
	}
	a.state = stateStopping
	a.mu.Unlock()

	var errs []error
	for i := len(a.cells) - 1; i >= 0; i-- {
		errs = append(errs, a.stopCellWithHooks(ctx, a.cells[i])...)
	}

	a.mu.Lock()
	a.state = stateStopped
	a.mu.Unlock()

	// Drain the async hook dispatcher after all cells have reported their
	// AfterStop events so shutdown telemetry lands before the process
	// exits. The drain is bounded by HookObserverDrainTimeout; a broken
	// observer does not indefinitely block Stop().
	if a.dispatcher != nil {
		a.dispatcher.stop(a.cfg.HookObserverDrainTimeout)
	}
	return errors.Join(errs...)
}

// Shutdown terminates background workers owned by the assembly without
// running Cell lifecycle stop. Intended for tests and for callers that
// constructed an assembly via New() but never invoked Start/Stop — it
// ensures the hook dispatcher goroutine does not linger.
//
// Shutdown is safe to call multiple times and is also implicitly invoked
// as the final step of Stop().
func (a *CoreAssembly) Shutdown() {
	if a.dispatcher != nil {
		a.dispatcher.stop(a.cfg.HookObserverDrainTimeout)
	}
}

// FlushHookEvents waits for the async hook dispatcher to process every
// event emitted so far, bounded by timeout. Returns true if drain
// completed, false if the timeout fired first. Intended for tests that
// need to observe deterministic observer state after a Start or Stop
// that may have failed (failed Start does not drain; only a successful
// Stop implicitly drains via HookObserverDrainTimeout).
//
// Zero timeout is interpreted as one second. Safe to call concurrently.
func (a *CoreAssembly) FlushHookEvents(timeout time.Duration) bool {
	if a.dispatcher == nil {
		return true
	}
	return a.dispatcher.flush(timeout)
}

// stopCellWithHooks executes BeforeStop → Stop → AfterStop for a single cell.
// All phases are best-effort: errors are accumulated but never abort the sequence.
// Logging is handled here — callers should not log the returned errors again.
//
// ref: runtime/worker/periodic.go runSafe — panic recovery pattern
func (a *CoreAssembly) stopCellWithHooks(ctx context.Context, c cell.Cell) []error {
	var errs []error
	if bs, ok := c.(cell.BeforeStopper); ok {
		if err := a.invokeHook(ctx, c.ID(), cell.HookBeforeStop, bs.BeforeStop); err != nil {
			slog.Warn("lifecycle: BeforeStop failed",
				slog.String("cell", c.ID()), slog.Any("error", err))
			errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: BeforeStop cell %q", c.ID()), err))
		}
	}
	if err := c.Stop(ctx); err != nil {
		slog.Warn("lifecycle: Stop failed",
			slog.String("cell", c.ID()), slog.Any("error", err))
		errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
			fmt.Sprintf("assembly: stop cell %q", c.ID()), err))
	}
	if as, ok := c.(cell.AfterStopper); ok {
		if err := a.invokeHook(ctx, c.ID(), cell.HookAfterStop, as.AfterStop); err != nil {
			slog.Warn("lifecycle: AfterStop failed",
				slog.String("cell", c.ID()), slog.Any("error", err))
			errs = append(errs, errcode.Wrap(errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: AfterStop cell %q", c.ID()), err))
		}
	}
	return errs
}

// Health returns the HealthStatus of every registered Cell, keyed by Cell ID.
func (a *CoreAssembly) Health() map[string]cell.HealthStatus {
	a.mu.Lock()
	snapshot := make([]cell.Cell, len(a.cells))
	copy(snapshot, a.cells)
	a.mu.Unlock()

	result := make(map[string]cell.HealthStatus, len(snapshot))
	for _, c := range snapshot {
		result[c.ID()] = c.Health()
	}
	return result
}

// StartWithConfig is like Start but injects the given config map into
// Dependencies.Config before initialising cells.
func (a *CoreAssembly) StartWithConfig(ctx context.Context, cfgMap map[string]any) error {
	return a.startInternal(ctx, cfgMap)
}

// startInternal is the shared implementation for Start and StartWithConfig.
// If cfgMap is nil an empty map is used for Dependencies.Config.
//
// ref: uber-go/fx app.go — Start 出错后自动 rollback 已启动的 Cell（LIFO Stop）。
func (a *CoreAssembly) startInternal(ctx context.Context, cfgMap map[string]any) error {
	a.mu.Lock()
	if a.state != stateStopped {
		a.mu.Unlock()
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot start in state %d", a.id, a.state))
	}
	a.state = stateStarting
	a.mu.Unlock()

	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}

	if err := cell.ValidateMode(a.cfg.DurabilityMode); err != nil {
		a.mu.Lock()
		a.state = stateStopped
		a.mu.Unlock()
		return errcode.Wrap(errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q", a.id), err)
	}

	deps := cell.Dependencies{
		Config:         cfgMap,
		DurabilityMode: a.cfg.DurabilityMode,
	}

	// Phase 1: Init all cells. If any fails, no cell has been Start'd yet.
	for _, c := range a.cells {
		if err := c.Init(ctx, deps); err != nil {
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.ErrValidationFailed,
				fmt.Sprintf("assembly: init cell %q", c.ID()), err)
		}
	}

	// Phase 2: Start cells in order with lifecycle hooks.
	// For each cell: BeforeStart → Start → AfterStart.
	// On any failure, rollback already-started cells in reverse (LIFO).
	//
	// ref: go-kratos/kratos app.go — BeforeStart/AfterStart hooks around server.Start
	// ref: uber-go/fx app.go — Start failure triggers LIFO rollback of started hooks
	for i, c := range a.cells {
		if err := a.startCellWithHooks(ctx, c, i); err != nil {
			return err
		}
	}

	a.mu.Lock()
	a.state = stateStarted
	a.mu.Unlock()
	return nil
}

// startCellWithHooks executes BeforeStart → Start → AfterStart for a single
// cell at index i. On any failure it rolls back cells [0..i-1] (and cell i
// itself if Start already succeeded) and transitions the assembly to stopped.
func (a *CoreAssembly) startCellWithHooks(ctx context.Context, c cell.Cell, i int) error {
	// BeforeStart hook (optional).
	if bs, ok := c.(cell.BeforeStarter); ok {
		slog.Info("lifecycle: BeforeStart", slog.String("cell", c.ID()))
		if err := a.invokeHook(ctx, c.ID(), cell.HookBeforeStart, bs.BeforeStart); err != nil {
			a.rollbackCells(ctx, i-1)
			return a.failStart(c.ID(), "BeforeStart", err)
		}
	}

	// Core Start.
	if err := c.Start(ctx); err != nil {
		a.rollbackCells(ctx, i-1)
		return a.failStart(c.ID(), "start", err)
	}

	// AfterStart hook (optional).
	// If this fails, the cell itself must be stopped because its Start
	// already succeeded — resources may have been acquired.
	if as, ok := c.(cell.AfterStarter); ok {
		slog.Info("lifecycle: AfterStart", slog.String("cell", c.ID()))
		if err := a.invokeHook(ctx, c.ID(), cell.HookAfterStart, as.AfterStart); err != nil {
			// Stop this cell first — its Start already succeeded.
			a.stopCellWithHooks(ctx, c) //nolint:errcheck // best-effort, logged inside
			a.rollbackCells(ctx, i-1)
			return a.failStart(c.ID(), "AfterStart", err)
		}
	}
	return nil
}

// failStart transitions the assembly to stopped and returns a wrapped error.
func (a *CoreAssembly) failStart(cellID, phase string, err error) error {
	a.mu.Lock()
	a.state = stateStopped
	a.mu.Unlock()
	return errcode.Wrap(errcode.ErrLifecycleInvalid,
		fmt.Sprintf("assembly: %s cell %q", phase, cellID), err)
}

// rollbackCells stops cells [0..upTo] in reverse order using stopCellWithHooks.
// All errors are logged inside stopCellWithHooks (best-effort, never abort).
func (a *CoreAssembly) rollbackCells(ctx context.Context, upTo int) {
	for j := upTo; j >= 0; j-- {
		a.stopCellWithHooks(ctx, a.cells[j]) //nolint:errcheck // best-effort, logged inside
	}
}

// callHookSafe invokes fn with panic recovery. Returns (err, panicked).
// When the hook panics, panicked is true and err carries the recovered
// message; outcome classification uses this to distinguish OutcomePanic
// from OutcomeFailure.
//
// ref: runtime/worker/periodic.go runSafe — same panic-to-error pattern
// ref: runtime/eventrouter/router.go — recover in subscription goroutine
func callHookSafe(fn func() error) (err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			err = fmt.Errorf("lifecycle hook panicked: %v", r)
		}
	}()
	err = fn()
	return
}

// invokeHook wraps ctx with HookTimeout (when > 0), invokes fn with panic
// recovery, classifies the outcome, and emits a HookEvent to the observer.
// Returns the hook error (nil on success) so callers can feed it to
// rollback/error-accumulation paths unchanged.
//
// Outcome classification:
//   - nil error                                            → OutcomeSuccess
//   - panic recovered                                      → OutcomePanic
//   - hookCtx deadline fired (regardless of err form)      → OutcomeTimeout
//   - err wraps context.DeadlineExceeded                   → OutcomeTimeout
//   - any other non-nil error                              → OutcomeFailure
//
// The hookCtx.Err() check catches hooks that create their own child context
// and return its error (e.g., context.Canceled) when the parent hookCtx
// deadline fires — errors.Is on the child error would miss the timeout.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go@master runStartHook — emit
// event around each hook, record runtime via clock.Now.
func (a *CoreAssembly) invokeHook(ctx context.Context, cellID string, phase cell.HookPhase, fn func(context.Context) error) error {
	hookCtx := ctx
	if a.cfg.HookTimeout > 0 {
		var cancel context.CancelFunc
		hookCtx, cancel = context.WithTimeout(ctx, a.cfg.HookTimeout)
		defer cancel()
	}

	start := time.Now()
	err, panicked := callHookSafe(func() error { return fn(hookCtx) })
	dur := time.Since(start)

	outcome := cell.OutcomeSuccess
	timedOut := hookCtx.Err() != nil && errors.Is(hookCtx.Err(), context.DeadlineExceeded)
	switch {
	case panicked:
		outcome = cell.OutcomePanic
	case err != nil && (timedOut || errors.Is(err, context.DeadlineExceeded)):
		outcome = cell.OutcomeTimeout
	case err != nil:
		outcome = cell.OutcomeFailure
	}

	a.emitHookEvent(cell.HookEvent{
		CellID:   cellID,
		Hook:     phase,
		Outcome:  outcome,
		Duration: dur,
		Err:      err,
	})
	return err
}

// emitHookEvent hands off the event to the async dispatcher. The
// dispatcher owns per-sink timeout + panic isolation + drop accounting so
// the assembly critical path returns immediately (ref: k8s.io/client-go
// tools/record/event.go@master — broadcaster fan-out).
//
// The fallback sync path below only runs when the dispatcher is nil — a
// theoretical post-refactor bug, not a production condition — and
// preserves the previous inline recover so a caller that bypasses New()
// still gets defense-in-depth.
func (a *CoreAssembly) emitHookEvent(e cell.HookEvent) {
	if a.dispatcher != nil {
		a.dispatcher.emit(e)
		return
	}
	// Defensive fallback — should not happen when New() is used.
	defer func() {
		if r := recover(); r != nil {
			var recoveredErr error
			if asErr, ok := r.(error); ok {
				recoveredErr = asErr
			} else {
				recoveredErr = fmt.Errorf("observer panicked: %v", r)
			}
			slog.Error("lifecycle: hook observer panicked",
				slog.String("cell", e.CellID),
				slog.String("hook", string(e.Hook)),
				slog.Any("error", recoveredErr))
		}
	}()
	a.cfg.HookObserver.OnHookEvent(e)
}

// CellIDs returns the IDs of all registered cells in registration order.
func (a *CoreAssembly) CellIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, len(a.cells))
	for i, c := range a.cells {
		ids[i] = c.ID()
	}
	return ids
}

// Cell returns the registered Cell with the given ID, or nil if not found.
func (a *CoreAssembly) Cell(id string) cell.Cell {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cellMap[id]
}
