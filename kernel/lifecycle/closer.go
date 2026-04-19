package lifecycle

import (
	"context"
	"io"
)

// ContextCloser wraps io.Closer with a context parameter so callers can share
// shutdown budgets (e.g., Uber fx StopTimeout, bootstrap shutCtx) across
// layered teardown chains.
//
// ref: uber-go/fx app.go Lifecycle.Append OnStop(ctx context.Context) error
// ref: rabbitmq/amqp091-go channel.go Channel.Close (IsClosed short-circuit)
type ContextCloser interface {
	Close(ctx context.Context) error
}

// IgnoreCtx adapts an io.Closer into a ContextCloser by discarding the ctx.
// Used as a bridge during incremental migration; callers that accept either
// interface should prefer a native ContextCloser implementation.
//
// IgnoreCtx(nil) returns nil — callers must check before calling Close.
func IgnoreCtx(c io.Closer) ContextCloser {
	if c == nil {
		return nil
	}
	return ctxCloserFunc(func(_ context.Context) error { return c.Close() })
}

// ctxCloserFunc is a function type that implements ContextCloser.
type ctxCloserFunc func(context.Context) error

func (f ctxCloserFunc) Close(ctx context.Context) error { return f(ctx) }
