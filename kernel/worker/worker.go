// Package worker defines the Worker domain contract.
//
// Implementations (WorkerGroup, LazyWorker, PeriodicWorker) live in
// runtime/worker/. kernel/worker/ is the interface-only package that
// adapters/ may depend on without pulling in runtime implementations,
// preserving the adapters→kernel-only dependency rule.
//
// ref: uber-go/fx lifecycle.go@master — Lifecycle interface defined in the
// public top-level package; implementations are internal.
// ref: go-zero core/service/servicegroup.go — Service contract separated
// from the group runner.
package worker

import (
	"context"
	"errors"
)

// ErrWorkerExitedEarly is the sentinel error returned by WorkerGroup when a
// member Worker.Start returns nil while the group context is still live.
// Workers are long-running; an early successful exit is indistinguishable
// from a silent cancellation, and the previous behaviour (record nil, leave
// firstErr unset) masked the abnormal state from operators. Modelling it as
// a typed error lets the group propagate the failure and lets callers
// errors.Is-check for it during shutdown reasoning.
var ErrWorkerExitedEarly = errors.New("worker: exited early without error before context cancellation")

// Worker represents a long-running background task.
//
// Contract:
//   - Start blocks until ctx is cancelled or the worker completes normally.
//     A non-nil error signals abnormal exit. A nil return from Start while
//     ctx is still live is itself an abnormal signal — the runtime
//     WorkerGroup converts that into ErrWorkerExitedEarly.
//   - Stop signals graceful shutdown, bounded by ctx. Should be idempotent.
type Worker interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
