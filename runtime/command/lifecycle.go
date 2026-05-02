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
	kcommand "github.com/ghbvf/gocell/kernel/command"
)

const defaultSweeperHookName = "command.sweeper"

// SweeperLifecycle exposes a kernel command Sweeper as a Cell lifecycle hook.
// OnStart launches the sweeper in a background goroutine; OnStop cancels it and
// waits for the goroutine to exit within the bootstrap stop budget.
//
// ref: uber-go/fx lifecycle Hook — start returns promptly; long-running work
// is owned by the hook and canceled from OnStop.
type SweeperLifecycle struct {
	Name         string
	Sweeper      *kcommand.Sweeper
	StartTimeout time.Duration
	StopTimeout  time.Duration
	Logger       *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan error
}

// NewSweeperLifecycle creates a lifecycle contributor for sweeper.
func NewSweeperLifecycle(name string, sweeper *kcommand.Sweeper) *SweeperLifecycle {
	return &SweeperLifecycle{Name: name, Sweeper: sweeper}
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
