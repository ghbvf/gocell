// Package shutdown provides graceful shutdown support by listening for OS
// signals (SIGINT, SIGTERM) and running registered cleanup hooks within a
// configurable timeout.
//
// The Manager executes hooks in registration order. If any hook returns an
// error, execution stops and the error is returned. If the timeout expires
// before all hooks complete, context.DeadlineExceeded is returned.
//
// Typical hook order mirrors the startup order in reverse:
//  1. Stop background workers (runtime/worker.WorkerGroup.Stop)
//  2. Drain HTTP connections (http.Server.Shutdown)
//  3. Stop the Cell assembly (kernel/assembly.CoreAssembly.Stop)
//  4. Close the config watcher (runtime/config)
//
// # Usage
//
//	mgr := shutdown.New(shutdown.WithTimeout(15 * time.Second))
//
//	mgr.Register(func(ctx context.Context) error {
//	    return httpServer.Shutdown(ctx)
//	})
//	mgr.Register(func(ctx context.Context) error {
//	    return asm.Stop(ctx)
//	})
//
//	// Blocks until SIGINT/SIGTERM, then runs hooks:
//	if err := mgr.Wait(); err != nil {
//	    slog.Error("graceful shutdown failed", slog.Any("error", err))
//	}
//
// For programmatic shutdown in tests, use Shutdown() instead of Wait().
package shutdown
