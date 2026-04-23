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
// deadline/cancellation fires. The name is used for structured logging —
// "<name>: closed" at Info on success, "<name>: close budget exceeded"
// at Warn on deadline expiry.
//
// If ctx is already done at entry, CloseWithDeadline returns ctx.Err()
// without invoking closeFn. This replaces the duplicated "check-ctx +
// goroutine + select + slog" pattern previously copied across
// postgres/redis/rabbitmq/vault adapters.
//
// closeFn returning a non-nil error is surfaced verbatim with no
// additional logging — adapters are expected to wrap sentinel codes
// (e.g. errcode.Wrap(ErrAdapterRedisConnect, ...)) before return.
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
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		if err != nil {
			return err
		}
		slog.Info(name + ": closed")
		return nil
	case <-ctx.Done():
		slog.Warn(name+": close budget exceeded",
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}
}
