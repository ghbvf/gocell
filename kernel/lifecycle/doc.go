// Package lifecycle provides the ContextCloser interface and adapters for
// managing resource teardown with context-aware shutdown budgets.
//
// # ContextCloser vs io.Closer
//
// Go's standard io.Closer only exposes Close() error, which prevents callers
// from sharing a single shutdown-budget context across multiple resources.
// When Bootstrap phase10 holds a shutCtx = context.WithTimeout(Background,
// shutdownTimeout), each io.Closer called without that ctx can independently
// hang, causing the total shutdown time to exceed the intended budget.
//
// ContextCloser solves this by accepting the ctx at call time:
//
//	type ContextCloser interface {
//	    Close(ctx context.Context) error
//	}
//
// # Migration guide
//
// Existing io.Closer implementations can be adapted incrementally:
//
//  1. Use IgnoreCtx to bridge an io.Closer into a ContextCloser without
//     any implementation change. The ctx is discarded, so the budget is
//     honoured only at the Bootstrap level, not within the resource.
//
//  2. Implement Close(ctx context.Context) error natively on the resource
//     so that ctx.Done() can abort long drain operations (e.g., waiting for
//     in-flight handlers to complete in rabbitmq.Subscriber).
//
// # Design decisions
//
//   - io.Closer fallback is preserved (via IgnoreCtx) so external dependencies
//     (pgx.Pool, redis.Client) that are io.Closer-only can participate in the
//     teardown chain without forking their types.
//   - resources that must complete teardown unconditionally (e.g., in-memory
//     channel close) should ignore the ctx intentionally and document the reason.
//
// ref: uber-go/fx app.go StopTimeout and Lifecycle.Append OnStop(ctx)
// ref: nats-io/nats.go Subscription.Drain (per-subscription state encapsulation)
package lifecycle
