// Package worker provides a Worker interface and WorkerGroup for managing
// concurrent background workers.
//
// ref: zeromicro/go-zero core/service/servicegroup.go — ServiceGroup pattern
// Adopted: parallel Start/Stop, goroutine-per-worker, sync.Once for stop.
// Deviated: context-based lifecycle (Start/Stop take ctx) instead of bare
// Start()/Stop(); error returns for diagnostics.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	kworker "github.com/ghbvf/gocell/kernel/worker"
)

// Worker is a type alias for the kernel Worker interface. The authoritative
// definition lives in kernel/worker; this alias lets the runtime/worker
// implementations (WorkerGroup, LazyWorker, PeriodicWorker) type-check
// against Worker without importing kernel/worker from every call site.
//
// This is not a migration shim — runtime/worker is the canonical package for
// Worker implementations, and the alias is how implementations declare they
// satisfy the kernel contract.
//
// Guidance for new consumers: code outside runtime/worker (e.g., adapters/,
// cells/) SHOULD import kernel/worker directly and reference worker.Worker
// (the kernel contract). This alias exists for historical compatibility and
// for runtime/worker's own implementations (WorkerGroup, LazyWorker,
// PeriodicWorker) to satisfy the kernel contract without importing the kernel
// package from every local file.
//
// ref: kernel/worker/worker.go — authoritative Worker interface definition.
type Worker = kworker.Worker

// WorkerGroup manages multiple workers, starting them concurrently and
// stopping them when requested.
type WorkerGroup struct {
	mu      sync.Mutex
	workers []Worker
}

// NewWorkerGroup creates a new WorkerGroup.
func NewWorkerGroup() *WorkerGroup {
	return &WorkerGroup{}
}

// Add registers a worker with the group.
func (g *WorkerGroup) Add(w Worker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.workers = append(g.workers, w)
}

// Start launches all workers concurrently. It blocks until all workers
// return. If any worker returns a non-context error, all sibling workers are
// cancelled via a shared context. The first error encountered is returned.
//
// Early-exit handling: a worker that returns nil while the group context is
// still live has terminated its long-running loop without signalling failure
// — historically that produced a silent firstErr=nil. The group now records
// kworker.ErrWorkerExitedEarly so callers can errors.Is-detect the abnormal
// signal. Returns from a Stop or context cancellation propagate untouched
// (errors.Is(err, context.Canceled) is honoured).
func (g *WorkerGroup) Start(ctx context.Context) error {
	g.mu.Lock()
	workers := make([]Worker, len(g.workers))
	copy(workers, g.workers)
	g.mu.Unlock()

	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)

	for _, w := range workers {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			err := w.Start(groupCtx)
			if err == nil && groupCtx.Err() == nil {
				err = kworker.ErrWorkerExitedEarly
			}
			if err == nil {
				return
			}
			slog.Error("worker exited with error",
				slog.String("worker_type", fmt.Sprintf("%T", w)),
				slog.Any("error", err))
			errOnce.Do(func() { firstErr = err })
			cancel() // cancel sibling workers
		}(w)
	}

	wg.Wait()
	return firstErr
}

// Stop stops all workers serially in reverse registration order.
// Each worker is stopped before the next to ensure safe teardown ordering.
func (g *WorkerGroup) Stop(ctx context.Context) error {
	g.mu.Lock()
	workers := make([]Worker, len(g.workers))
	copy(workers, g.workers)
	g.mu.Unlock()

	var firstErr error
	// Stop in reverse order, serially.
	for i := len(workers) - 1; i >= 0; i-- {
		if err := workers[i].Stop(ctx); err != nil {
			slog.Error("worker stop failed", slog.Any("error", err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
