package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
)

// phaseError wraps a teardown error with the name of the component/phase that
// produced it. This makes post-mortem diagnosis unambiguous when multiple
// teardown steps can fail and the error is logged or inspected via errors.As.
//
// Used by LIFO teardown and startup rollback to attach the teardown step name
// to each non-nil error, mirroring the eventrouter.phaseError pattern.
//
// ref: uber-go/fx app.go StopTimeout — per-hook error surfacing.
type phaseError struct {
	Phase string
	Err   error
}

func (e *phaseError) Error() string {
	return e.Phase + ": " + e.Err.Error()
}
func (e *phaseError) Unwrap() error { return e.Err }

// shutdownReason indicates what triggered the phase9 shutdown signal.
// Used by phase10 to decide whether to surface error details.
type shutdownReason int

const (
	// reasonCtxCancel: external context was canceled (normal Kubernetes pod termination).
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

// namedTeardown pairs a teardown function with a diagnostic label.
// The label appears in phaseError when the teardown returns a non-nil error,
// making post-mortem diagnosis unambiguous without trawling logs.
type namedTeardown struct {
	name string
	fn   func(context.Context) error
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
	teardowns []namedTeardown

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

// addTeardown appends a teardown function with an optional diagnostic label.
// The label is used in phaseError when the teardown returns a non-nil error.
// If name is empty, the teardown still runs but errors won't carry phase context.
func (s *runState) addTeardown(fn func(context.Context) error) {
	s.teardowns = append(s.teardowns, namedTeardown{name: "", fn: fn})
}

// addNamedTeardown appends a teardown function with a diagnostic label.
// Used by addCloser so that error messages identify the resource type.
func (s *runState) addNamedTeardown(name string, fn func(context.Context) error) {
	s.teardowns = append(s.teardowns, namedTeardown{name: name, fn: fn})
}

// safeTeardown executes a single named teardown, recovering from any panic.
// A panicking teardown is converted to an error so LIFO execution continues
// to the next step without the panic escaping. The caller is responsible for
// wrapping the returned error in *phaseError when a phase label is required.
//
// ref: runtime/worker/periodic.go runSafe — recover→error, no re-panic.
// ref: kernel/assembly/assembly.go — per-step recover in LIFO teardown loop.
func safeTeardown(ctx context.Context, td namedTeardown) (err error) {
	defer func() {
		if r := recover(); r != nil {
			rerr, _ := r.(error)
			if rerr == nil {
				rerr = fmt.Errorf("panic: %v", r)
			}
			err = rerr
		}
	}()
	err = td.fn(ctx)
	return
}

// rollback runs teardowns in LIFO order on startup failure (all in one budget),
// cancels runCtx, and returns cause joined with all teardown errors (including
// those recovered from panicking teardowns). cause is always first in the
// joined tree so errors.Is(result, cause) holds. Panicking teardowns do not
// interrupt LIFO rollback — each step is protected by safeTeardown.
//
// ref: runtime/bootstrap/lifecycle.go:183 — errors.Join(append([]error{err}, rollbackErrs...)...).
// ref: runtime/worker/periodic.go runSafe — recover→error, no re-panic.
// ref: uber-go/fx app.go withRollback — every started component must be torn
// down in reverse even when a later step never succeeded.
func (s *runState) rollback(shutCtx context.Context, cause error) error {
	slog.Error("bootstrap: startup failed, rolling back",
		slog.String("error", cause.Error()),
		slog.Int("teardowns_pending", len(s.teardowns)))
	var rollbackErrs []error
	for _, v := range slices.Backward(s.teardowns) {
		td := v
		if err := safeTeardown(shutCtx, td); err != nil {
			if td.name != "" {
				err = &phaseError{Phase: "teardown_" + td.name, Err: err}
			}
			slog.Warn("bootstrap: rollback step failed", slog.Any("error", err))
			rollbackErrs = append(rollbackErrs, err)
		}
	}
	s.runCancel()
	return errors.Join(append([]error{cause}, rollbackErrs...)...)
}
