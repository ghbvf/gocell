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
	"log/slog"
	"sync"

	kworker "github.com/ghbvf/gocell/kernel/worker"
)

// Worker is a type alias for the kernel Worker interface. Provided so that
// existing code referencing runtime/worker.Worker continues to compile, while
// kernel/worker defines the authoritative contract.
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
			if err := w.Start(groupCtx); err != nil {
				slog.Error("worker exited with error", slog.Any("error", err))
				errOnce.Do(func() { firstErr = err })
				cancel() // cancel sibling workers
			}
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
