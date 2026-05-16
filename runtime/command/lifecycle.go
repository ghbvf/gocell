// Package command wires kernel command workers into runtime lifecycle hooks.
package command

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/validation"
)

// sweeperReadinessChecker is the optional no-side-effect readiness contract a
// SweepTicker may implement. SweeperLifecycle.Start invokes it before spawning
// the loop so a misconstructed sweeper (e.g. the zero-value &command.Sweeper{})
// fails at OnStart — bootstrap rolls back — instead of starting "successfully"
// and only erroring on the first (logged-and-swallowed) tick (review P2-1).
//
// *kcommand.Sweeper implements it via Sweeper.Validate.
type sweeperReadinessChecker interface {
	Validate() error
}

// defaultCommandSweeperInterval is the default ticker period when callers do
// not supply an explicit interval.
const defaultCommandSweeperInterval = 30 * time.Second

const defaultSweeperHookName = "command.sweeper"

// startProbeTimeout is the window given to the sweeper goroutine after launch
// to surface an immediate startup failure. If the goroutine exits within this
// window with an error, lifecycle.Start propagates it to the caller.
//
// ref: runtime/outbox/relay.go — readyCh pattern (relay blocks in Start;
// sweeper is fire-and-forget so we use a time-bounded probe instead).
const startProbeTimeout = 50 * time.Millisecond

// controlPlaneTicker creates a real-time ticker for control-plane scheduling.
// All stdlib time.NewTicker calls in this package are funneled here.
//
// Carve-out rationale: control-plane scheduling (sweeper ticker interval) must
// use real wall-clock time. Injecting a fake clock into the control-plane ticker
// reintroduces the startup-deadlock regression fixed in C.1: a frozen fake clock
// with no Advance calls blocks Start() permanently.
//
// AI-rebust grade: Medium (comment-guard carve-out). Hard upgrade path:
// backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
//
// Do NOT add new functions with this marker to bypass the Hard upgrade.
// New time.* calls here require updating the backlog item and explicit review.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
//
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1 (CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)
func controlPlaneTicker(interval time.Duration) *time.Ticker {
	return time.NewTicker(interval)
}

// controlPlaneProbeTimer creates a real-time timer for the startup probe window.
// All stdlib time.NewTimer calls in this package are funneled here.
//
// Carve-out rationale: the 50 ms startup probe must use real time. A fake clock
// probe that is never advanced would deadlock Start() (same root cause as C.1).
//
// AI-rebust grade: Medium (comment-guard carve-out). Hard upgrade path:
// backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
//
// Do NOT add new functions with this marker to bypass the Hard upgrade.
// New time.* calls here require updating the backlog item and explicit review.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
//
//archtest:allow:clock-injection:control-plane startup-deadlock-regression-C1 (CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)
func controlPlaneProbeTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d)
}

// SweepTicker is the minimal interface consumed by SweeperLifecycle.
// It replaces the old SweeperRunner (Start/Stop) interface: the worker loop
// now lives in SweeperLifecycle and calls SweepTick on every ticker fire.
//
// *kcommand.Sweeper satisfies SweepTicker; tests may inject mocks directly.
type SweepTicker interface {
	SweepTick(ctx context.Context, now time.Time) error
}

// Compile-time check: *kcommand.Sweeper satisfies SweepTicker.
var _ SweepTicker = (*kcommand.Sweeper)(nil)

// SweeperLifecycle exposes a kernel command Sweeper as a Cell lifecycle hook.
// OnStart launches a real-time ticker loop in a background goroutine; OnStop
// cancels it and waits for the goroutine to exit within the bootstrap stop
// budget.
//
// OnStart must return promptly (spawn goroutine + fast probe, then return).
// Blocking OnStart occupies the entire lifecycle.Start chain and stalls all
// subsequent hooks. SweeperLifecycle is the reference implementation of the
// LifecycleHook.OnStart fast-return contract: it spawns the loop, awaits the
// first-tick probe (≤50 ms), then returns.
//
// C.1 control-plane/business-plane clock split: kernel/command.Sweeper holds
// NO clock field (Hard, type-unrepresentable). SweeperLifecycle holds a
// BusinessClock used ONLY for the SweepTick `now` argument (business-plane
// expiry time). All control-plane stdlib time.* calls (ticker, startup probe)
// are funneled through controlPlaneTicker / controlPlaneProbeTimer
// (function-level marker //archtest:allow:clock-injection:control-plane,
// anchored by PROD-CLOCK-INJECTION-01) — control-plane scheduling is never
// driven by an injected clock. Backlog:
// CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
//
// C.2 Owner ctx: OnStart receives the long-lived owner ctx (controller-runtime
// Runnable.Start semantics). The worker derives its runCtx from ownerCtx so
// that assembly shutdown (ownerCancel) exits the goroutine even without an
// explicit OnStop call.
//
// C.3 Observable errors: SweepTick errors are logged via slog.Error (with
// slog.String("cell", CellID)) and optionally counted via SweepErrorCounter
// (With(Labels{"cell": CellID})). CellID defaults to "_runtime" when empty,
// aligning with the observability.md sentinel convention.
//
// ref: uber-go/fx lifecycle Hook — start returns promptly; long-running work
// is owned by the hook and canceled from OnStop.
// ref: kubernetes-sigs/controller-runtime Runnable.Start — OnStart receives
// the long-lived manager ctx.
type SweeperLifecycle struct {
	Name         string
	Sweeper      SweepTicker
	Interval     time.Duration
	StartTimeout time.Duration
	// StartTimeout is informational only; the runner does not enforce it as an
	// OnStart ctx deadline (see ADR 202605102000 §D1 RETRACTED). It is used only
	// for the slow-start warning threshold in the lifecycle runner.
	StopTimeout time.Duration
	Logger      *slog.Logger

	// BusinessClock supplies the business-plane "now" passed to SweepTick (the
	// time against which command deadlines are evaluated). It is read-only here
	// (only .Now() is called) and is NEVER used for control-plane scheduling —
	// the ticker / startup probe stay on real wall-clock via controlPlaneTicker
	// / controlPlaneProbeTimer. Sourcing `now` from the real-time ticker.C
	// instead (the pre-fix behavior) mismatched a cell whose command-creation
	// time came from an injected (e.g. fake) clock, corrupting expiry decisions
	// in fake-clock assemblies (review P2-2). Inject the cell clock here
	// (devicecell passes c.clk); kernel/command.Sweeper itself still holds NO
	// clock field (ADR 202605170000 §D-A Hard invariant unchanged).
	BusinessClock clock.Clock

	// CellID is the owner cell identifier for observability labels and log fields.
	// Inject from the composition root (e.g. cell.ID()). Defaults to "_runtime"
	// sentinel when empty, aligning with the observability.md cell label convention.
	CellID string

	// SweepErrorCounter is an optional pre-bound CounterVec. When non-nil,
	// it is incremented (With({"cell": CellID})) on every SweepTick error.
	// Inject via composition root; leave nil to disable counter tracking.
	SweepErrorCounter kernelmetrics.CounterVec

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSweeperLifecycle creates a lifecycle contributor for sweeper.
// interval is the ticker period; if zero, defaultCommandSweeperInterval is used.
//
// businessClock supplies the business-plane "now" for SweepTick. It is NOT a
// control-plane scheduling clock: the ticker / startup probe remain on real
// wall-clock via controlPlaneTicker / controlPlaneProbeTimer (function-level
// //archtest:allow:clock-injection:control-plane marker, anchored by
// PROD-CLOCK-INJECTION-01). Backlog: CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
// Validated non-nil at construction via clock.MustHaveClock (composition root
// passes the cell clock, e.g. clock.Real() in production / a fake in tests).
//
// The sweeper parameter accepts any SweepTicker; *kcommand.Sweeper is the
// primary implementation. Tests may inject mocks directly.
func NewSweeperLifecycle(name string, sweeper SweepTicker, interval time.Duration, businessClock clock.Clock) *SweeperLifecycle {
	clock.MustHaveClock(businessClock, "command.NewSweeperLifecycle: businessClock required")
	if interval <= 0 {
		interval = defaultCommandSweeperInterval
	}
	return &SweeperLifecycle{Name: name, Sweeper: sweeper, Interval: interval, BusinessClock: businessClock}
}

// Hook returns the single lifecycle hook managed by SweeperLifecycle.
func (l *SweeperLifecycle) Hook() cell.LifecycleHook {
	return cell.LifecycleHook{
		Name:         l.hookName(),
		OnStart:      l.Start,
		OnStop:       l.Stop,
		StartTimeout: l.StartTimeout,
		StopTimeout:  l.StopTimeout,
	}
}

// Start launches the sweeper loop and returns after the goroutine is running.
//
// C.2: ownerCtx is the long-lived assembly owner ctx (controller-runtime
// Runnable.Start semantics). The worker derives its runCtx from ownerCtx
// so that assembly shutdown (ownerCancel) exits the goroutine automatically,
// even before OnStop is called.
//
// All stdlib time.* calls are funneled through controlPlaneTicker /
// controlPlaneProbeTimer (function-level marker
// //archtest:allow:clock-injection:control-plane). Backlog:
// CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
func (l *SweeperLifecycle) Start(ownerCtx context.Context) error {
	if l == nil || validation.IsNilInterface(l.Sweeper) {
		return fmt.Errorf("runtime/command: sweeper lifecycle requires non-nil Sweeper")
	}

	// P2-2: business-plane now source. Defensive nil check — NewSweeperLifecycle
	// validates via clock.MustHaveClock, but a struct-literal composer may omit
	// it; returning a typed error keeps OnStart fail-fast (bootstrap rolls back)
	// instead of a nil deref in runLoop.
	if validation.IsNilInterface(l.BusinessClock) {
		return fmt.Errorf("runtime/command: sweeper lifecycle requires non-nil BusinessClock " +
			"(use NewSweeperLifecycle, or set BusinessClock on the struct literal)")
	}

	// P2-1: no-side-effect readiness gate. A zero-value &command.Sweeper{}
	// (or any SweepTicker exposing Validate) must fail here so bootstrap rolls
	// back, rather than starting and silently erroring on every tick.
	if rc, ok := l.Sweeper.(sweeperReadinessChecker); ok {
		if err := rc.Validate(); err != nil {
			return fmt.Errorf("runtime/command: sweeper not ready: %w", err)
		}
	}

	// P1-4: preflight the optional error counter's label set. CounterVec.With
	// panics (MustValidateLabels) on a label-set mismatch; the first SweepTick
	// error would otherwise crash the (recover-less) loop goroutine and
	// crashloop the service. Probe .With here under recover so a misconfigured
	// counter is a fail-fast wiring error at OnStart, never a runtime crash.
	if l.SweepErrorCounter != nil {
		if err := l.preflightSweepErrorCounter(); err != nil {
			return err
		}
	}

	interval := l.Interval
	if interval <= 0 {
		interval = defaultCommandSweeperInterval
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil // already started — idempotent
	}

	// C.2: derive worker ctx from owner ctx, not context.Background().
	// ownerCancel exits the goroutine when the assembly shuts down.
	runCtx, cancel := context.WithCancel(ownerCtx)
	done := make(chan struct{})
	l.cancel = cancel
	l.done = done

	// earlyExit carries the first error if the worker exits before the probe
	// window closes. Buffered 1 so the goroutine never blocks on send.
	earlyExit := make(chan error, 1)

	// C.1: real-time ticker — not injected via business-plane clock.
	// Funneled through controlPlaneTicker (function-level marker).
	ticker := controlPlaneTicker(interval)
	go l.runLoop(runCtx, ticker, earlyExit, done)

	// awaitProbe waits for the goroutine startup signal or context cancellation.
	if err := l.awaitProbe(runCtx, cancel, earlyExit); err != nil {
		return err
	}

	// l.cancel is nil if awaitProbe hit the runCtx.Done() branch (owner ctx
	// canceled before first tick); the goroutine is self-exiting, not "started".
	if l.cancel != nil {
		l.logger().Info("runtime/command: sweeper started",
			slog.String("hook", l.hookName()),
			slog.String("cell", l.cellID()))
	}
	return nil
}

// runLoop is the sweeper goroutine body. It calls SweepTick on every ticker
// fire and signals earlyExit on the first tick so the startup probe can return.
func (l *SweeperLifecycle) runLoop(
	runCtx context.Context,
	ticker *time.Ticker,
	earlyExit chan<- error,
	done chan struct{},
) {
	defer close(done)
	defer ticker.Stop()
	started := false
	for {
		select {
		case <-runCtx.Done():
			return
		case <-ticker.C:
			if !started {
				started = true
				earlyExit <- nil // signal that the goroutine is running
			}
			// P2-2: business-plane now (against which command deadlines are
			// evaluated) comes from BusinessClock, NOT the real-time ticker's
			// tick value — keeps expiry consistent with a cell whose command
			// creation time uses the same (possibly fake) injected clock.
			now := l.BusinessClock.Now()
			if err := l.Sweeper.SweepTick(runCtx, now); err != nil {
				l.logger().Error("runtime/command: SweepTick error",
					slog.String("hook", l.hookName()),
					slog.String("cell", l.cellID()),
					slog.Any("error", err))
				if l.SweepErrorCounter != nil {
					l.SweepErrorCounter.With(kernelmetrics.Labels{"cell": l.cellID()}).Inc()
				}
			}
		}
	}
}

// awaitProbe waits up to startProbeTimeout for the sweeper goroutine to signal
// readiness (first tick fired). Returns nil in all non-error cases.
//
// earlyExit semantics: runLoop always sends nil on first tick as a startup
// signal; it never sends a non-nil error (fire-and-forget design — SweepTick
// failures are logged but do not propagate through earlyExit). The channel
// exists solely to confirm the goroutine is alive and has received its first
// tick, distinguishing "goroutine launched and ticked" from "probe window
// elapsed before first tick" (both are normal operations).
//
// Three outcomes:
//
//  1. First tick fires within startProbeTimeout: earlyExit receives nil. The
//     goroutine is confirmed running. Start returns nil.
//
//  2. Probe window elapses (common case: interval >> startProbeTimeout): Start
//     returns nil. The goroutine is running — the ticker just hasn't fired yet.
//
//  3. ownerCtx canceled before probe window closed: Start logs a Warn (owner
//     ctx canceled before sweeper confirmed running) and returns nil. The
//     goroutine will exit via runCtx.Done() on its own; l.cancel / l.done are
//     cleared so a subsequent Stop is a no-op.
//
// C.1: probe timer is funneled through controlPlaneProbeTimer.
func (l *SweeperLifecycle) awaitProbe(
	runCtx context.Context,
	cancel context.CancelFunc,
	earlyExit <-chan error,
) error {
	probeTimer := controlPlaneProbeTimer(startProbeTimeout)
	defer probeTimer.Stop()

	select {
	case <-earlyExit:
		// First tick fired within probe window — goroutine is confirmed running.
		// earlyExit always carries nil (fire-and-forget semantics; see godoc).
	case <-probeTimer.C:
		// Probe window elapsed without first tick (interval > startProbeTimeout,
		// which is the common case). Goroutine is running normally.
	case <-runCtx.Done():
		// Owner ctx was canceled before the probe window closed and before the
		// goroutine fired its first tick. The goroutine will exit via runCtx.Done().
		// Start still returns nil — cancellation is not an error from Start's
		// perspective. l.cancel / l.done are cleared so Stop is a no-op.
		l.logger().Warn("runtime/command: sweeper owner ctx canceled before first tick",
			slog.String("hook", l.hookName()),
			slog.String("cell", l.cellID()))
		cancel()
		l.cancel = nil
		l.done = nil
	}
	return nil
}

// Stop cancels the sweeper and waits for its goroutine to exit.
func (l *SweeperLifecycle) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.cancel = nil
	l.done = nil
	l.mu.Unlock()

	if cancel == nil {
		return nil
	}
	cancel()

	select {
	case <-done:
		l.logger().Info("runtime/command: sweeper stopped",
			slog.String("hook", l.hookName()),
			slog.String("cell", l.cellID()))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// preflightSweepErrorCounter exercises SweepErrorCounter.With with the exact
// label set runLoop uses, under recover. CounterVec.With panics on a label-set
// mismatch (kernelmetrics.MustValidateLabels); catching it here converts a
// crash-on-first-error wiring bug into a fail-fast OnStart error so bootstrap
// rolls back at startup (review P1-4). No counter value is mutated (.Inc is
// not called) — .With alone triggers label validation.
func (l *SweeperLifecycle) preflightSweepErrorCounter() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("runtime/command: SweepErrorCounter label set invalid "+
				"(must be exactly {\"cell\"}): %v", r)
		}
	}()
	_ = l.SweepErrorCounter.With(kernelmetrics.Labels{"cell": l.cellID()})
	return nil
}

// cellID returns the cell ID for observability labels. Defaults to "_runtime"
// sentinel when CellID is empty, aligning with the observability.md convention.
func (l *SweeperLifecycle) cellID() string {
	if l != nil && l.CellID != "" {
		return l.CellID
	}
	return "_runtime"
}

func (l *SweeperLifecycle) hookName() string {
	if l != nil && l.Name != "" {
		return l.Name
	}
	return defaultSweeperHookName
}

func (l *SweeperLifecycle) logger() *slog.Logger {
	if l != nil && l.Logger != nil {
		return l.Logger
	}
	return slog.Default()
}
