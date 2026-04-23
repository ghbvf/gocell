// Package adapterutil centralizes shared plumbing helpers used by GoCell
// adapters (postgres / redis / rabbitmq / vault). It depends only on
// stdlib so it can be imported from any adapter package without
// introducing cross-adapter coupling.
package adapterutil

import (
	"context"
	"log/slog"
)

// CloseWithDeadline runs closeFn in a goroutine and returns whichever
// completes first: closeFn's result or ctx.Err() when the context's
// deadline/cancellation fires. The name is used for structured logging.
//
// If ctx is already done at entry, CloseWithDeadline returns ctx.Err()
// without invoking closeFn. This replaces the duplicated "check-ctx +
// goroutine + select + slog" pattern previously copied across
// postgres/redis/rabbitmq/vault adapters.
//
// closeFn returning a non-nil error is surfaced verbatim with no
// additional logging when the receiver is still waiting. If the deadline
// has already fired and closeFn returns an error after the fact, that
// error is logged at Warn under "adapter close error after budget
// exceeded" so operators can correlate late failures with the timeout.
// On successful completion the helper also logs at Info under
// "adapter closed" so operators can correlate adapter shutdowns
// with the surrounding lifecycle events.
//
// Callers that own background goroutines (reconnect loops, watchdogs)
// must signal those goroutines (e.g. close a stop channel) BEFORE
// invoking CloseWithDeadline. A pre-cancelled ctx short-circuits the
// helper without invoking closeFn — any pre-work the caller wanted to
// run unconditionally must precede the helper call.
//
// ref: uber-go/fx app.go StopTimeout — shared shutdown budget pattern.
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser semantics.
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close — closeCh
// signal then conn.Close() under the caller's budget.
func CloseWithDeadline(ctx context.Context, name string, closeFn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := closeFn()
		select {
		case done <- err:
		default:
			// Deadline already fired and the receiver returned. Surface the
			// late close error so operators can see what eventually happened.
			if err != nil {
				slog.Warn("adapter close error after budget exceeded",
					slog.String("component", name),
					slog.Any("error", err))
			}
		}
	}()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
		slog.Info("adapter closed", slog.String("component", name))
		return nil
	case <-ctx.Done():
		slog.Warn("adapter close budget exceeded",
			slog.String("component", name),
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}
}
