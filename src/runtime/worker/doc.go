// Package worker provides a Worker interface and WorkerGroup for managing
// concurrent background tasks in GoCell applications.
//
// Worker represents any long-running background task. WorkerGroup starts all
// registered workers concurrently and stops them in reverse registration order
// during graceful shutdown. If a worker exits with an error, the error is logged
// and the first error is returned from Start.
//
// The package also provides NewPeriodicWorker for recurring scheduled jobs.
// Periodic workers respect context cancellation and do not drift: each tick
// fires at a fixed interval from the previous execution start.
//
// ref: zeromicro/go-zero core/service/servicegroup.go — ServiceGroup pattern
// Adopted: parallel Start/Stop, goroutine-per-worker.
// Deviated: context-based lifecycle (Start/Stop take ctx); error returns for
// diagnostics; reverse-order stop for ordered teardown.
//
// # Usage
//
//	wg := worker.NewWorkerGroup()
//	wg.Add(myConsumerWorker)
//	wg.Add(worker.NewPeriodicWorker(5*time.Minute, func(ctx context.Context) error {
//	    return runCleanupJob(ctx)
//	}))
//
//	// Start all workers (blocks until all return):
//	if err := wg.Start(ctx); err != nil {
//	    slog.Error("worker group failed", slog.Any("error", err))
//	}
//
//	// Stop all workers gracefully (e.g., from shutdown.Hook):
//	wg.Stop(shutdownCtx)
package worker
