// Package command wires kernel command workers into runtime lifecycle hooks.
package command

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
)

const defaultSweeperHookName = "command.sweeper"

// startProbeTimeout is the window given to the sweeper goroutine after launch
// to surface an immediate startup failure. If the goroutine exits within this
// window with an error, lifecycle.Start propagates it to the caller.
// The value is chosen to be large enough for in-process failures (misconfigured
// sweeper, nil queue, …) but small enough not to delay a healthy startup.
//
// ref: runtime/outbox/relay.go — readyCh pattern (relay blocks in Start;
// sweeper is fire-and-forget so we use a time-bounded probe instead).
const startProbeTimeout = 50 * time.Millisecond

// SweeperRunner is the minimal interface consumed by SweeperLifecycle.
// *kcommand.Sweeper satisfies it; tests may inject mocks directly.
type SweeperRunner interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Compile-time check: *kcommand.Sweeper satisfies SweeperRunner.
var _ SweeperRunner = (*kcommand.Sweeper)(nil)

// SweeperLifecycle exposes a kernel command Sweeper as a Cell lifecycle hook.
// OnStart launches the sweeper in a background goroutine; OnStop cancels it and
// waits for the goroutine to exit within the bootstrap stop budget.
//
// ref: uber-go/fx lifecycle Hook — start returns promptly; long-running work
// is owned by the hook and canceled from OnStop.
type SweeperLifecycle struct {
	Name         string
	Sweeper      SweeperRunner
	StartTimeout time.Duration
	StopTimeout  time.Duration
	Logger       *slog.Logger
	Clock        clock.Clock

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan error
}

// NewSweeperLifecycle creates a lifecycle contributor for sweeper.
// clk must be non-nil; it is validated at construction time via clock.MustHaveClock.
// The composition root constructs the Clock (clock.Real() or a test double) and
// passes it here; lifecycle.go does not call clock.Real() directly.
func NewSweeperLifecycle(name string, sweeper *kcommand.Sweeper, clk clock.Clock) *SweeperLifecycle {
	clock.MustHaveClock(clk, "command.NewSweeperLifecycle: clock required")
	return &SweeperLifecycle{Name: name, Sweeper: sweeper, Clock: clk}
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
// The OnStart ctx parameter is intentionally ignored — per
// docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md, the
// LifecycleHook.OnStart ctx carries startup-deadline semantics (paired
// with cell.LifecycleHook.StartTimeout). The worker goroutine derives its
// runCtx from context.Background() so that OnStart returning (= startup
// budget exhausted) does not cancel the long-running sweeper. The sole
// worker-cancel path is OnStop, which bootstrap LIFO rollback guarantees
// to invoke on already-started hooks (contract pinned by
// runtime/command/lifecycle_rollback_test.go::TestSweeperLifecycle_
// StartupFailRollback).
//
// Backlog LIFECYCLE-OWNER-CTX-PROPAGATION-01 tracks the controller-runtime
// owner-ctx propagation alternative (worker derived from a shared main
// lifecycle ctx) — gated behind explicit trigger conditions; do not
// silently relax the current contract.
func (l *SweeperLifecycle) Start(_ context.Context) error {
	if l == nil || l.Sweeper == nil {
		return fmt.Errorf("runtime/command: sweeper lifecycle requires non-nil Sweeper")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	l.cancel = cancel
	l.done = done

	go func() {
		err := l.Sweeper.Start(runCtx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		done <- err
	}()

	// Startup probe: give the goroutine a brief window to surface an immediate
	// failure (misconfigured sweeper, nil dependency, …). If the goroutine
	// exits within startProbeTimeout with an error, propagate it to the caller
	// so bootstrap can abort and roll back instead of silently swallowing the
	// error in the background.
	//
	// ref: runtime/outbox/relay.go readyCh pattern — relay blocks in Start so
	// it can close readyCh synchronously; sweeper is fire-and-forget so we use
	// a time-bounded probe as the equivalent synchronization point.
	select {
	case err := <-done:
		// Goroutine exited before the probe window closed.
		if err != nil {
			// Cancel the run ctx (belt-and-suspenders cleanup) and reset state
			// so a subsequent Start call is not blocked by a stale cancel.
			cancel()
			l.cancel = nil
			l.done = nil
			return fmt.Errorf("runtime/command: sweeper failed on startup: %w", err)
		}
		// Goroutine exited without error (e.g. ctx was already canceled) —
		// treat as a clean early exit; leave cancel/done nil so Stop is a no-op.
		l.cancel = nil
		l.done = nil
		return nil
	case <-l.clk().NewTimerAt(l.clk().Now().Add(startProbeTimeout)).C():
		// Probe window elapsed — sweeper is running normally.
	}

	l.logger().Info("runtime/command: sweeper started", slog.String("hook", l.hookName()))
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
	case err := <-done:
		if err != nil {
			return fmt.Errorf("runtime/command: sweeper stopped with error: %w", err)
		}
		l.logger().Info("runtime/command: sweeper stopped", slog.String("hook", l.hookName()))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

func (l *SweeperLifecycle) clk() clock.Clock {
	return l.Clock
}
