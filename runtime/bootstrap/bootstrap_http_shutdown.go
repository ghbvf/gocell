package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// shutdownCtxFor derives the per-server shutdown context from the parent ctx
// and the listener's shutGrace setting.
//
//   - shutGrace > 0 wraps the parent with context.WithTimeout(parent, shutGrace).
//     context.WithTimeout already bounds the resulting deadline to whichever of
//     parent.Deadline() and (now + shutGrace) comes first, so the global
//     shutdownTimeout always wins when shutGrace exceeds it (R2-03).
//   - shutGrace == 0 returns the parent unchanged (no per-listener override; the
//     server inherits the global shutdownTimeout). The returned cancel is a
//     no-op so callers can defer it unconditionally.
func shutdownCtxFor(parent context.Context, shutGrace time.Duration) (context.Context, context.CancelFunc) {
	if shutGrace > 0 {
		return context.WithTimeout(parent, shutGrace)
	}
	return parent, noopShutdownCancel
}

// noopShutdownCancel is the cancel function returned by shutdownCtxFor when
// shutGrace == 0. There is nothing to cancel because no derived context exists,
// but callers always defer the returned cancel.
func noopShutdownCancel() {
	// Intentionally empty: shutGrace == 0 returns the parent context unchanged.
}

// shutdownTask represents a single server shutdown operation with its name and
// grace period. Tests can inject arbitrary shutdown functions without requiring
// a real http.Server.
type shutdownTask struct {
	name       string
	shutGrace  time.Duration
	shutdown   func(context.Context) error
	forceClose func() error
	stopped    <-chan struct{}
}

// shutdownAllServers drains all servers in parallel. Each task uses a
// per-server context derived from the parent ctx:
//   - when shutGrace > 0 (set via WithListenerShutdownGrace), the parent ctx
//     is wrapped with context.WithTimeout(ctx, shutGrace) so shutGrace is an
//     upper bound within the global shutdownTimeout budget.
//   - when shutGrace == 0, the shared ctx is passed through unchanged.
//
// Errors are aggregated via errors.Join so operators see every failure.
func shutdownAllServers(ctx context.Context, tasks []shutdownTask) error {
	slog.Info("bootstrap: draining HTTP servers")
	resultCh := make(chan error, len(tasks))
	for _, task := range tasks {
		go func() {
			err := shutdownServerTask(ctx, task)
			if err != nil {
				slog.Error("bootstrap: HTTP server drain failed",
					slog.String("listener", task.name), slog.Any("error", err))
				err = fmt.Errorf("listener %q shutdown: %w", task.name, err)
			} else {
				slog.Info("bootstrap: HTTP server drained", slog.String("listener", task.name))
			}
			resultCh <- err
		}()
	}
	var errs []error
	for range tasks {
		if err := <-resultCh; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func shutdownServerTask(parent context.Context, task shutdownTask) error {
	if task.shutdown == nil {
		return waitForServerStopped(parent, task)
	}
	shutCtx, cancel := shutdownCtxFor(parent, task.shutGrace)
	defer cancel()
	err := task.shutdown(shutCtx)
	if err != nil {
		if shutCtx.Err() != nil {
			err = errors.Join(err, forceCloseServer(task), waitForServerStoppedAfterForceClose(parent, task))
		}
		return err
	}
	return waitForServerStopped(shutCtx, task)
}

func waitForServerStopped(ctx context.Context, task shutdownTask) error {
	if task.stopped == nil {
		return nil
	}
	select {
	case <-task.stopped:
		return nil
	case <-ctx.Done():
		return errors.Join(ctx.Err(), forceCloseServer(task))
	}
}

func waitForServerStoppedAfterForceClose(ctx context.Context, task shutdownTask) error {
	if task.stopped == nil {
		return nil
	}
	select {
	case <-task.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func forceCloseServer(task shutdownTask) error {
	if task.forceClose == nil {
		return nil
	}
	slog.Warn("bootstrap: HTTP server graceful shutdown timed out; forcing close",
		slog.String("listener", task.name))
	return task.forceClose()
}

// boundServersToTasks converts []boundServer into []shutdownTask for shutdownAllServers.
func boundServersToTasks(servers []boundServer) []shutdownTask {
	tasks := make([]shutdownTask, len(servers))
	for i, bs := range servers {
		tasks[i] = shutdownTask{
			name:       bs.name,
			shutGrace:  bs.shutGrace,
			shutdown:   bs.srv.Shutdown,
			forceClose: bs.srv.Close,
			stopped:    bs.stopped,
		}
	}
	return tasks
}
