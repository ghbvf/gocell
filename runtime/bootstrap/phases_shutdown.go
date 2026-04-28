package bootstrap

// phases_shutdown.go — signal waiting and graceful shutdown orchestration
// (phases 9–10).
//
// Covers:
//   - phase9AwaitShutdownSignal: blocks on ctx cancel / HTTP error / worker error / router error
//   - drainHTTPErrors: collects all buffered HTTP errors before joining
//   - phase10OrchestrateShutdown: three-stage LIFO teardown (readiness flip → pre-shutdown delay → components)
//   - phase10ReadinessFlip: health handler SetShuttingDown + optional pre-shutdown delay
//   - phase10LIFOTeardown: LIFO teardown with per-component error collection
//
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go —
// engageStopProcedure: LIFO teardown + StopAndWait + signal coordination.
// The three-stage shutdown model (readiness flip → pre-shutdown delay →
// component teardown) mirrors controller-runtime's graceful-stop sequence.

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// drainHTTPErrors collects the first error and any additional errors already
// buffered in ch, then joins them. Called only after receiving the first error
// from httpErrCh so the channel is guaranteed non-empty at entry.
func drainHTTPErrors(ch <-chan error, first error) error {
	allErrs := []error{first}
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return errors.Join(allErrs...)
			}
			if e != nil {
				allErrs = append(allErrs, e)
			}
		default:
			return errors.Join(allErrs...)
		}
	}
}

// phase9AwaitShutdownSignal blocks until one of: external ctx cancel, HTTP error,
// worker error, or router error. It returns a shutdownSignal describing what fired.
// It does NOT cancel workerCtx or runCtx — that happens in phase10.
//
// CORR-04: after receiving the first HTTP error from httpErrCh, drain any remaining
// errors and join them so no error is silently discarded.
func (b *Bootstrap) phase9AwaitShutdownSignal(ctx context.Context, s *phaseState) shutdownSignal {
	slog.Info("bootstrap: application started successfully")
	select {
	case <-ctx.Done():
		slog.Info("bootstrap: context cancelled, shutting down")
		return shutdownSignal{reason: reasonCtxCancel}
	case firstErr := <-s.httpErrCh:
		return shutdownSignal{reason: reasonHTTPError, err: drainHTTPErrors(s.httpErrCh, firstErr)}
	case err := <-s.workerErrCh:
		if err != nil {
			slog.Error("bootstrap: worker failed, initiating shutdown",
				slog.String("component", "worker"),
				slog.Any("error", err))
		}
		return shutdownSignal{reason: reasonWorkerError, err: err}
	case err := <-s.routerErrCh:
		if err != nil {
			slog.Error("bootstrap: event router failed, initiating shutdown",
				slog.String("component", "event_router"),
				slog.Any("error", err))
		}
		return shutdownSignal{reason: reasonRouterError, err: err}
	}
}

// phase10OrchestrateShutdown executes the three-stage LIFO shutdown:
//
//  1. Readiness flip (SetShuttingDown so LBs drain traffic)
//  2. Pre-shutdown delay (optional, shares the total shutdownTimeout budget)
//  3. LIFO teardown of all registered components
//
// If the incoming signal carries a non-nil error (HTTP/worker/router failure) AND
// phase10 teardown itself is clean, the signal error is still returned to the
// caller so Run() surfaces the triggering failure.
//
// ref: sigs.k8s.io/controller-runtime engageStopProcedure (LIFO + StopAndWait)
// ref: uber-go/fx app.go StopTimeout
// ref: Kubernetes pod shutdown model (preStop counts toward terminationGracePeriodSeconds)
func (b *Bootstrap) phase10OrchestrateShutdown(s *phaseState, sig shutdownSignal) error {
	shutCtx, cancel := context.WithTimeout(context.Background(), b.lifecycle.shutdownTimeout)
	defer cancel()

	m := b.metrics.shutdownMet
	totalStart := time.Now()

	// --- stage 1: readiness flip ---
	m.recordPhaseEntry(shutdownPhaseReadinessFlip)
	flipStart := time.Now()
	b.phase10ReadinessFlip(shutCtx, s)
	m.observePhaseDuration(shutdownPhaseReadinessFlip, time.Since(flipStart))

	// --- stage 2: LIFO teardown ---
	m.recordPhaseEntry(shutdownPhaseLIFOTeardown)
	tearStart := time.Now()
	teardownErrs := b.phase10LIFOTeardown(shutCtx, s)
	m.observePhaseDuration(shutdownPhaseLIFOTeardown, time.Since(tearStart))

	// --- stage 3: finalize ---
	m.recordPhaseEntry(shutdownPhaseClosed)
	m.observePhaseDuration(shutdownPhaseTotal, time.Since(totalStart))

	// F3: outcome reflects the final return semantics, not just ctx state.
	// Precedence: timeout > teardown_error > signal_error > success.
	//   - timeout       : shutCtx expired during any stage; worst case for SREs.
	//   - teardown_error: at least one teardown returned non-nil (non-timeout).
	//   - signal_error  : shutdown was triggered by an HTTP/worker/router error,
	//                     teardown itself was clean.
	//   - success       : user-initiated shutdown with clean teardown.
	teardownErr := errors.Join(teardownErrs...)
	outcome := "success"
	switch {
	case shutCtx.Err() != nil:
		outcome = "timeout"
	case teardownErr != nil:
		outcome = "teardown_error"
	case sig.err != nil:
		outcome = "signal_error"
	}
	m.countOutcome(outcome)

	// Safety net: cancel runCtx after all teardowns complete so any goroutine
	// still holding runCtx eventually unblocks.
	s.runCancel()

	if teardownErr != nil {
		return teardownErr
	}
	// Surface the triggering signal error when teardown itself was clean.
	return sig.err
}

// phase10ReadinessFlip marks the health handler as shutting down (503) and
// waits for the preShutdownDelay, sharing the shutCtx budget.
func (b *Bootstrap) phase10ReadinessFlip(shutCtx context.Context, s *phaseState) {
	slog.Info("bootstrap: initiating graceful shutdown")
	if s.reloads != nil {
		// early signal: prevents new reload callbacks from entering the gate;
		// the returned drained channel is intentionally not awaited here.
		// Full drain (BeginShutdown + drain + ctx.Done) happens in the phase3
		// teardown closure registered in phase3InitAssembly, which executes
		// during phase10LIFOTeardown at the end of the shutdown sequence.
		s.reloads.BeginShutdown()
	}
	if s.hh != nil {
		s.hh.SetShuttingDown()
	}

	if b.lifecycle.preShutdownDelay <= 0 {
		return
	}
	slog.Info("bootstrap: pre-shutdown drain delay",
		slog.Duration("delay", b.lifecycle.preShutdownDelay))
	select {
	case <-time.After(b.lifecycle.preShutdownDelay):
	case <-shutCtx.Done():
	}
}

// phase10LIFOTeardown runs all teardown functions in reverse registration order.
// Errors are collected but do not abort remaining teardowns (best-effort cleanup).
// Each non-nil error is wrapped in a phaseError with the component name so that
// post-mortem diagnosis can pinpoint the failing resource without trawling logs.
//
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go engageStopProcedure — LIFO.
func (b *Bootstrap) phase10LIFOTeardown(shutCtx context.Context, s *phaseState) []error {
	var errs []error
	for i := len(s.teardowns) - 1; i >= 0; i-- {
		td := s.teardowns[i]
		if err := td.fn(shutCtx); err != nil {
			if td.name != "" {
				err = &phaseError{Phase: "teardown_" + td.name, Err: err}
			}
			slog.Error("bootstrap: shutdown step failed",
				slog.String("phase", td.name),
				slog.Any("error", err))
			errs = append(errs, err)
		}
	}
	return errs
}
