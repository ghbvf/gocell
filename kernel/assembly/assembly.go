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
	"slices"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
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

// DefaultReloadTimeout is the per-invocation deadline applied to each
// OnConfigReload callback when Config.ReloadTimeout is zero. Deliberately the
// same value as DefaultHookTimeout so reload callbacks share the same timeout
// budget as lifecycle hooks — but named independently to avoid semantic
// confusion between lifecycle hooks and config-reload callbacks.
//
// ref: etcd clientv3 Watch ctx propagation — watchers propagate the caller's ctx.
// ref: k8s SharedInformer ctx propagation — informer callbacks carry caller ctx.
const DefaultReloadTimeout = DefaultHookTimeout

// Config holds assembly-level configuration.
type Config struct {
	ID             string
	DurabilityMode cell.DurabilityMode // Required: Demo or Durable (zero value rejected by CheckNotNoop)

	// Clock is the time source used for hook-runtime measurement and any
	// other time-dependent assembly bookkeeping. Required: New panics when
	// Clock is nil so that missing wiring fails fast at construction time
	// rather than masquerading as wall-clock-driven flakiness in tests.
	// Production wiring passes clock.Real(); tests inject clockmock.New(...).
	Clock clock.Clock

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

	// HookObserverDrainTimeout bounds Stop()'s shared hook-dispatcher
	// shutdown budget after event intake is closed. The same budget covers
	// both queue drain (worker processing already-emitted events) and sink
	// drain (observer goroutines that exceeded HookObserverSinkTimeout but
	// later exit). Zero uses DefaultHookObserverDrainTimeout (5s). Stop(ctx)
	// also returns early when ctx is canceled; Shutdown uses
	// context.Background() and is bounded only by this timeout. After drain
	// completion, timeout, or cancellation, any further emit() calls are
	// counted as queue_full.
	HookObserverDrainTimeout time.Duration

	// MetricsProvider receives the dispatcher's internal drop / queue-depth
	// metrics. Nil uses metrics.NopProvider, preserving the prior
	// zero-dependency default. Wire a real provider to make dropped events
	// visible in dashboards.
	MetricsProvider metrics.Provider

	// ReloadTimeout bounds each OnConfigReload callback invocation. Zero uses
	// DefaultReloadTimeout. Negative disables the per-invocation timeout
	// (callbacks inherit the parent ctx's deadline only).
	//
	// Semantics mirror HookTimeout: soft-cancel — the ctx passed to the
	// callback is canceled when the deadline fires, but the callback goroutine
	// continues until it observes ctx. A well-behaved callback should return
	// promptly when ctx.Done() closes.
	//
	// ref: etcd clientv3 Watch — watchers propagate the caller's ctx.
	// ref: k8s SharedInformer — informer callbacks carry a bounded ctx.
	ReloadTimeout time.Duration
}

// CoreAssembly is the default Assembly implementation. It manages a set of
// Cells, starting them in registration order and stopping them in reverse.
type CoreAssembly struct {
	mu                sync.Mutex
	id                string
	cfg               Config
	cells             []cell.Cell
	cellMap           map[string]cell.Cell
	state             assemblyState
	dispatcher        *hookDispatcher                  // owned by the current Start/Stop cycle
	dispatcherDropped metrics.CounterVec               // registered once; reused when dispatcher is rebuilt
	snapshots         map[string]cell.RegistrySnapshot // keyed by cell ID; populated during startInternal
}

// New creates a CoreAssembly with the given configuration.
//
// cfg.Clock is required: assembly.New panics when Clock is nil OR a
// typed-nil interface (e.g. (*realClock)(nil) wrapped in a clock.Clock
// value). The single root clock is constructed once at the composition
// root via clock.Real() and threaded through every assembly + cell;
// missing wiring fails fast at construction time rather than masquerading
// as wall-clock-driven flakiness in tests. Tests inject clockmock.New(...).
//
// If cfg.HookObserver is nil, cell.NopHookObserver{} is substituted so the
// hook call sites can emit unconditionally.
// If cfg.HookTimeout is zero, DefaultHookTimeout is applied. Negative value
// disables per-hook timeout entirely.
func New(cfg Config) *CoreAssembly {
	clock.MustHaveClock(cfg.Clock, "assembly.New")
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
	if cfg.ReloadTimeout == 0 {
		cfg.ReloadTimeout = DefaultReloadTimeout
	}
	// Eagerly construct the async dispatcher at New() time (not lazy on first
	// emit) so its lifetime is deterministic: callers that construct an
	// assembly and never Start it can still call Shutdown to drain cleanly, and
	// goleak-based tests cannot witness a racy lazy-start.
	dispatcher := newHookDispatcher(newDispatcherConfig(cfg, nil))
	return &CoreAssembly{
		id:                cfg.ID,
		cfg:               cfg,
		cellMap:           make(map[string]cell.Cell),
		snapshots:         make(map[string]cell.RegistrySnapshot),
		dispatcher:        dispatcher,
		dispatcherDropped: dispatcher.dropped,
	}
}

func newDispatcherConfig(cfg Config, dropped metrics.CounterVec) dispatcherConfig {
	return dispatcherConfig{
		Observer:    cfg.HookObserver,
		QueueSize:   cfg.HookObserverQueueSize,
		SinkTimeout: cfg.HookObserverSinkTimeout,
		Provider:    cfg.MetricsProvider,
		Dropped:     dropped,
		Clock:       cfg.Clock,
	}
}

func (a *CoreAssembly) ensureDispatcherLocked() {
	if a.dispatcher != nil {
		return
	}
	a.dispatcher = newHookDispatcher(newDispatcherConfig(a.cfg, a.dispatcherDropped))
	if a.dispatcherDropped == nil {
		a.dispatcherDropped = a.dispatcher.dropped
	}
}

func (a *CoreAssembly) currentDispatcher() *hookDispatcher {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dispatcher
}

// Register adds a Cell to the assembly. It returns an error if the Cell ID is
// empty, already registered, or the assembly has already been started.
func (a *CoreAssembly) Register(c cell.Cell) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state != stateStopped {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot register in state %d", a.id, a.state))
	}

	if c == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "cell must not be nil")
	}

	id := c.ID()
	if id == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "cell ID must not be empty")
	}
	if _, exists := a.cellMap[id]; exists {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			fmt.Sprintf("duplicate cell ID: %q", id))
	}
	a.cells = append(a.cells, c)
	a.cellMap[id] = c
	return nil
}

// Start initializes and starts every registered Cell in registration order.
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
	for _, v := range slices.Backward(a.cells) {
		errs = append(errs, a.stopCellWithHooks(ctx, v)...)
	}

	a.mu.Lock()
	dispatcher := a.dispatcher
	a.mu.Unlock()

	// Drain the async hook dispatcher after all cells have reported their
	// AfterStop events so shutdown telemetry lands before the process
	// exits. The drain is bounded by ctx and HookObserverDrainTimeout; a
	// broken observer does not indefinitely block Stop().
	if dispatcher != nil {
		dispatcher.stop(ctx, a.cfg.HookObserverDrainTimeout)
	}

	a.mu.Lock()
	a.state = stateStopped
	// Clear the remaining snapshot entries after all cells have stopped.
	// stopCellWithHooks deletes each cell's entry individually; this final
	// reset covers any entries that may have been added concurrently or
	// missed due to rollback paths.
	a.snapshots = make(map[string]cell.RegistrySnapshot)
	if a.dispatcher == dispatcher {
		a.dispatcher = nil
	}
	a.mu.Unlock()
	return errors.Join(errs...)
}

// Shutdown terminates background workers owned by the assembly without
// running Cell lifecycle stop. Intended for tests and for callers that
// constructed an assembly via New() but never invoked Start/Stop — it
// ensures the hook dispatcher goroutine does not linger.
//
// Shutdown is safe to call multiple times. Stop drains the dispatcher for
// normal lifecycle shutdown; Shutdown is the explicit teardown path for
// assemblies that never reached a successful Start/Stop cycle.
func (a *CoreAssembly) Shutdown() {
	a.mu.Lock()
	dispatcher := a.dispatcher
	a.dispatcher = nil
	a.mu.Unlock()
	if dispatcher != nil {
		dispatcher.stop(context.Background(), a.cfg.HookObserverDrainTimeout)
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
	dispatcher := a.currentDispatcher()
	if dispatcher == nil {
		return true
	}
	return dispatcher.flush(timeout)
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
			errs = append(errs, errcode.Wrap(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: BeforeStop cell %q", c.ID()), err))
		}
	}
	if err := c.Stop(ctx); err != nil {
		slog.Warn("lifecycle: Stop failed",
			slog.String("cell", c.ID()), slog.Any("error", err))
		errs = append(errs, errcode.Wrap(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
			fmt.Sprintf("assembly: stop cell %q", c.ID()), err))
	}
	if as, ok := c.(cell.AfterStopper); ok {
		if err := a.invokeHook(ctx, c.ID(), cell.HookAfterStop, as.AfterStop); err != nil {
			slog.Warn("lifecycle: AfterStop failed",
				slog.String("cell", c.ID()), slog.Any("error", err))
			errs = append(errs, errcode.Wrap(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
				fmt.Sprintf("assembly: AfterStop cell %q", c.ID()), err))
		}
	}
	// Remove the stopped cell's snapshot so Snapshots() never returns stale
	// data for cells that are no longer running.
	a.mu.Lock()
	delete(a.snapshots, c.ID())
	a.mu.Unlock()
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
// Dependencies.Config before initializing cells.
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
		// Snapshot the offending state inside the lock so the error message
		// reflects the value we observed at the guard, not whatever a racing
		// Stop()/Start() goroutine writes after we release a.mu.
		observedState := a.state
		a.mu.Unlock()
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q: cannot start in state %d", a.id, observedState))
	}
	a.ensureDispatcherLocked()
	a.state = stateStarting
	a.mu.Unlock()

	if cfgMap == nil {
		cfgMap = make(map[string]any)
	}

	if err := cell.ValidateMode(a.cfg.DurabilityMode); err != nil {
		a.mu.Lock()
		a.state = stateStopped
		a.mu.Unlock()
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			fmt.Sprintf("assembly %q", a.id), err)
	}

	// Phase 1: Init all cells via per-cell RegistryRecorder.
	// Each cell declares its capabilities (routes, subscriptions, health, lifecycle,
	// config-reload) into the recorder; Snapshot() seals it and stores the result.
	// If any Init fails, no cell has been Start'd yet — safe to return immediately.
	//
	// Each cell receives an independent deep copy of cfgMap so that one cell's
	// Init or Start path cannot mutate the config seen by another cell.
	// ref: spf13/viper AllSettings() returns a deep copy for the same reason;
	//      k8s client-go DeepCopyObject — each consumer owns its own value.
	//
	// Snapshots are accumulated into a local map and published to a.snapshots
	// under a.mu only after every cell.Start succeeds. Publishing at the same
	// lifecycle boundary as stateStarted keeps Snapshots() from exposing
	// startup metadata that may still be rolled back. Writing the shared map
	// inside the loop without the lock would race with concurrent Snapshots()
	// readers — Go runtime treats concurrent map read/write as fatal.
	// ref: PR-V1-030-K01 (review G1-01).
	localSnaps := make(map[string]cell.RegistrySnapshot, len(a.cells))
	for _, c := range a.cells {
		recorder := cell.NewRegistryRecorder(cloneConfigMap(cfgMap), a.cfg.DurabilityMode)
		if err := c.Init(ctx, recorder); err != nil {
			// a.snapshots is untouched until the post-loop publish below, so
			// the failure path only needs to roll the state back. localSnaps
			// goes out of scope and is reclaimed.
			a.mu.Lock()
			a.state = stateStopped
			a.mu.Unlock()
			return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
				fmt.Sprintf("assembly: init cell %q", c.ID()), err)
		}
		localSnaps[c.ID()] = recorder.Snapshot()
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
	a.snapshots = localSnaps
	a.state = stateStarted
	a.mu.Unlock()
	return nil
}

// startCellWithHooks executes BeforeStart → Start → AfterStart for a single
// cell at index i. On any failure it rolls back cells [0..i-1] (and cell i
// itself if Start already succeeded) and transitions the assembly to stopped.
//
// Rollback derives its own ctx from context.Background(); it never reuses the
// caller's startCtx. See rollbackCells for the rationale.
func (a *CoreAssembly) startCellWithHooks(ctx context.Context, c cell.Cell, i int) error {
	// BeforeStart hook (optional).
	if bs, ok := c.(cell.BeforeStarter); ok {
		slog.Info("lifecycle: BeforeStart", slog.String("cell", c.ID()))
		if err := a.invokeHook(ctx, c.ID(), cell.HookBeforeStart, bs.BeforeStart); err != nil {
			a.rollbackCells(i - 1)
			return a.failStart(c.ID(), "BeforeStart", err)
		}
	}

	// Core Start.
	if err := c.Start(ctx); err != nil {
		a.rollbackCells(i - 1)
		return a.failStart(c.ID(), "start", err)
	}

	// AfterStart hook (optional).
	// If this fails, the cell itself must be stopped because its Start
	// already succeeded — resources may have been acquired. rollbackCells(i)
	// (note: i, not i-1) includes the failing cell at index i in the LIFO
	// teardown so all stops — including this cell's — share one HookTimeout
	// budget under a single rollback ctx.
	if as, ok := c.(cell.AfterStarter); ok {
		slog.Info("lifecycle: AfterStart", slog.String("cell", c.ID()))
		if err := a.invokeHook(ctx, c.ID(), cell.HookAfterStart, as.AfterStart); err != nil {
			a.rollbackCells(i)
			return a.failStart(c.ID(), "AfterStart", err)
		}
	}
	return nil
}

// failStart transitions the assembly to stopped, clears the snapshots map
// (since the assembly did not reach the Started state, the snapshot data is
// no longer valid), and returns a wrapped error.
//
// Snapshots() documents this invariant: it returns nil when the
// assembly is not in the running state.
func (a *CoreAssembly) failStart(cellID, phase string, err error) error {
	a.mu.Lock()
	a.state = stateStopped
	a.snapshots = make(map[string]cell.RegistrySnapshot)
	a.mu.Unlock()
	return errcode.Wrap(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
		fmt.Sprintf("assembly: %s cell %q", phase, cellID), err)
}

// rollbackCells stops cells [0..upTo] in reverse order using stopCellWithHooks.
// All errors are logged inside stopCellWithHooks (best-effort, never abort).
//
// Rollback derives its own ctx from context.Background() so a SIGTERM that
// cancels the caller's startCtx does not also cancel teardown. fx's
// withRollback (uber-go/fx app.go) reuses the start ctx and is broken in this
// case — see ADR docs/architecture/202605051800-adr-rollback-ctx-decoupling.md.
//
// The single rollback root ctx is shared across all cells in this rollback so
// the total wallclock budget is bounded by cfg.HookTimeout, matching K8s
// terminationGracePeriodSeconds expectations. Per-hook deadlines further
// shrink this budget inside invokeHook.
//
// upTo < 0 means no cells were started yet (e.g. BeforeStart on cell index 0
// failed) — return early to skip a no-op ctx allocation.
func (a *CoreAssembly) rollbackCells(upTo int) {
	if upTo < 0 {
		return
	}
	ctx, cancel := a.newRollbackCtx()
	defer cancel()
	for j := upTo; j >= 0; j-- {
		a.stopCellWithHooks(ctx, a.cells[j])
	}
}

// newRollbackCtx returns a fresh ctx for rollback teardown, decoupled from any
// caller-supplied startCtx. Deadline derivation:
//
//   - cfg.HookTimeout > 0    → context.WithTimeout(Background, HookTimeout)
//   - cfg.HookTimeout == 0   → context.WithTimeout(Background, DefaultHookTimeout)
//   - cfg.HookTimeout  < 0   → context.WithCancel(Background) (no deadline,
//     mirrors invokeHook's "negative disables" semantic; callers still get a
//     valid CancelFunc to release resources)
//
// Cancel is the caller's responsibility; pair every newRollbackCtx with a
// matching cancel call.
func (a *CoreAssembly) newRollbackCtx() (context.Context, context.CancelFunc) {
	timeout := a.cfg.HookTimeout
	if timeout == 0 {
		timeout = DefaultHookTimeout
	}
	if timeout < 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
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
	// Start the wall-clock before WithTimeout so Duration spans the full
	// ctx-setup + hook-execution window. Otherwise the clock reads slightly
	// after the deadline timer arms, making Duration < HookTimeout even when
	// the hook blocked until the deadline fired — breaks assertions like
	// Duration >= HookTimeout under scheduling jitter.
	start := a.cfg.Clock.Now()
	hookCtx := ctx
	if a.cfg.HookTimeout > 0 {
		var cancel context.CancelFunc
		hookCtx, cancel = context.WithTimeout(ctx, a.cfg.HookTimeout)
		defer cancel()
	}

	err, panicked := callHookSafe(func() error { return fn(hookCtx) })
	dur := a.cfg.Clock.Since(start)

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
	if dispatcher := a.currentDispatcher(); dispatcher != nil {
		dispatcher.emit(e)
		return
	}
	// Defensive fallback — should not happen when New() is used.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("lifecycle: hook observer panicked",
				slog.String("cell", e.CellID),
				slog.String("hook", string(e.Hook)),
				slog.String("panic_type", hookObserverPanicType(r)),
				slog.String("panic", sanitizeHookObserverPanicValue(r)))
		}
	}()
	a.cfg.HookObserver.OnHookEvent(e)
}

// ID returns the assembly's identifier as set in Config.ID.
func (a *CoreAssembly) ID() string {
	return a.id
}

// Clock returns the single root [clock.Clock] used by the assembly for
// lifecycle hook timing. Exposed so that Bootstrap can fail-fast when a
// caller passes both WithAssembly and WithClock with non-identical clock
// instances.
//
// Always non-nil — assembly.New rejects nil and typed-nil at construction.
func (a *CoreAssembly) Clock() clock.Clock {
	return a.cfg.Clock
}

// ReloadTimeout returns the per-invocation deadline applied to each
// OnConfigReload callback as configured by Config.ReloadTimeout. Always
// non-zero after construction (zero is normalized to DefaultReloadTimeout by
// New). Negative values disable the per-invocation timeout.
func (a *CoreAssembly) ReloadTimeout() time.Duration {
	return a.cfg.ReloadTimeout
}

// Snapshots returns the per-cell registry declarations recorded during Init.
// Returns nil when assembly is not in running state (starting, Init failure,
// Start failure, stopping, or after Stop).
//
// The returned map is a copy keyed by cell ID — mutations do not affect the
// assembly's internal state. The RegistrySnapshot values themselves are not
// deep-copied; callers must not mutate the slice/map fields inside each snapshot.
//
// Invariant: Snapshots() returns a non-empty map only when the assembly is in
// stateStarted. failStart, Stop, and rollbackCells all clear the map so that
// callers never observe stale snapshots from a prior run.
//
// ref: uber-go/fx App.Done lifecycle state machine — state gates access to
// component metadata.
func (a *CoreAssembly) Snapshots() map[string]cell.RegistrySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != stateStarted || len(a.snapshots) == 0 {
		return nil
	}
	cp := make(map[string]cell.RegistrySnapshot, len(a.snapshots))
	for k, v := range a.snapshots {
		cp[k] = v
	}
	return cp
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

// cloneConfigMap returns a deep copy of a map[string]any config snapshot so
// that each cell's Init call receives an independent value — mutations in one
// cell's Init path cannot affect sibling cells.
//
// Three value shapes are handled recursively:
//   - map[string]any — recurse into nested maps
//   - []any — recurse into slice elements
//   - scalar (string, bool, number, nil, etc.) — copy by value (no reference)
//
// reflect is intentionally not used; type assertions are sufficient for the
// config value space and avoid allocating reflect.Value temporaries.
//
// ref: spf13/viper AllSettings() — returns a deep copy of its internal map.
// ref: k8s.io/apimachinery runtime.DefaultUnstructuredConverter — deep-copies
//
//	map[string]interface{} config trees without reflect.
func cloneConfigMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	dst := make(map[string]any, len(m))
	for k, v := range m {
		dst[k] = cloneConfigValue(v)
	}
	return dst
}

// cloneConfigValue deep-copies a single config value.
func cloneConfigValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneConfigMap(t)
	case []any:
		if t == nil {
			return nil
		}
		dst := make([]any, len(t))
		for i, elem := range t {
			dst[i] = cloneConfigValue(elem)
		}
		return dst
	default:
		// Scalars (string, bool, int64, float64, nil, etc.) are value types —
		// copying by assignment is safe.
		return v
	}
}
