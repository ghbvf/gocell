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
)

// Worker represents a long-running background task.
type Worker interface {
	// Start begins the worker. It should block until ctx is cancelled or
	// the worker completes. Returning a non-nil error signals abnormal exit.
	Start(ctx context.Context) error
	// Stop signals the worker to shut down gracefully.
	Stop(ctx context.Context) error
}

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
// return. If any worker returns an error, it is logged. The first error
// encountered is returned.
func (g *WorkerGroup) Start(ctx context.Context) error {
	g.mu.Lock()
	workers := make([]Worker, len(g.workers))
	copy(workers, g.workers)
	g.mu.Unlock()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		firstErr error
	)

	for _, w := range workers {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			if err := w.Start(ctx); err != nil {
				slog.Error("worker exited with error", slog.Any("error", err))
				errOnce.Do(func() { firstErr = err })
			}
		}(w)
	}

	wg.Wait()
	return firstErr
}

// Stop stops all workers concurrently in reverse registration order.
func (g *WorkerGroup) Stop(ctx context.Context) error {
	g.mu.Lock()
	workers := make([]Worker, len(g.workers))
	copy(workers, g.workers)
	g.mu.Unlock()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		firstErr error
	)

	// Stop in reverse order.
	for i := len(workers) - 1; i >= 0; i-- {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			if err := w.Stop(ctx); err != nil {
				slog.Error("worker stop failed", slog.Any("error", err))
				errOnce.Do(func() { firstErr = err })
			}
		}(workers[i])
	}

	wg.Wait()
	return firstErr
}
