package bootstrap

import (
	"context"
	"log/slog"
)

// shutdownReason indicates what triggered the phase9 shutdown signal.
// Used by phase10 to decide whether to surface error details.
type shutdownReason int

const (
	// reasonCtxCancel: external context was cancelled (normal Kubernetes pod termination).
	reasonCtxCancel shutdownReason = iota
	// reasonHTTPError: the HTTP server goroutine returned an unexpected error.
	reasonHTTPError
	// reasonWorkerError: a background worker returned a non-nil error.
	reasonWorkerError
	// reasonRouterError: the event router goroutine returned a non-nil error.
	reasonRouterError
)

// shutdownSignal bundles the reason with the originating error (if any).
type shutdownSignal struct {
	reason shutdownReason
	err    error // non-nil for reasonHTTPError / reasonWorkerError / reasonRouterError
}

// runState holds mutable runtime state accumulated during Run().
// Split from bootstrap.go to keep the main Run() method focused on
// phase orchestration rather than state management.
//
// The owned run context is NOT stored on the struct (Go idiom: don't embed
// context.Context in a struct). It is created by newRunState and returned
// alongside, then threaded explicitly into phase6StartEventRouter and
// phase8StartWorkers. Cancellation is still reachable via runCancel, which
// phase10 / rollback invoke.
//
// The run context is derived from context.Background() (NOT from the external
// ctx) so that external ctx cancellation triggers orderly phase10 shutdown
// rather than propagating directly to worker/eventRouter goroutines.
//
// ref: uber-go/fx app.go:L545-567 (run vs stop ctx separation).
type runState struct {
	runCancel context.CancelFunc

	// teardowns accumulates cleanup functions in registration order;
	// phase10LIFOTeardown executes them in reverse (LIFO).
	// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go (engageStopProcedure LIFO teardown).
	teardowns []func(context.Context) error

	// channels wired during phase6/7/8; awaited in phase9.
	// nil channels are never selected (Go select semantics).
	httpErrCh   chan error
	workerErrCh chan error
	routerErrCh chan error
}

// newRunState creates a runState and its owned run context. The caller must
// call runCancel (or rely on defer state.runCancel() in Run) to release
// resources.
func newRunState() (context.Context, *runState) {
	rc, cancel := context.WithCancel(context.Background())
	return rc, &runState{
		runCancel: cancel,
	}
}

// addTeardown appends a teardown function that will be called LIFO during
// phase10 or rollback.
func (s *runState) addTeardown(fn func(context.Context) error) {
	s.teardowns = append(s.teardowns, fn)
}

// rollback runs teardowns in LIFO order on startup failure (all in one budget),
// cancels runCtx, and returns the original cause error.
// ref: uber-go/fx app.go withRollback — every started component must be
// torn down in reverse even when a later step never succeeded.
func (s *runState) rollback(shutCtx context.Context, cause error) error {
	slog.Error("bootstrap: startup failed, rolling back", slog.Any("error", cause))
	for i := len(s.teardowns) - 1; i >= 0; i-- {
		if err := s.teardowns[i](shutCtx); err != nil {
			slog.Warn("bootstrap: rollback step failed", slog.Any("error", err))
		}
	}
	s.runCancel()
	return cause
}
