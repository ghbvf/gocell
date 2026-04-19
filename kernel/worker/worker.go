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

import "context"

// Worker represents a long-running background task.
//
// Contract:
//   - Start blocks until ctx is cancelled or the worker completes normally.
//     A non-nil error signals abnormal exit.
//   - Stop signals graceful shutdown, bounded by ctx. Should be idempotent.
type Worker interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
