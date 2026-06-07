package grpc

import "context"

// drain.go — framework-side gRPC drain signal (GAP-1 PR-10 [#1153]).
//
// DrainSignal is a one-shot, idempotent shutdown signal shared between the
// adapters/grpc.Server (producer: Trigger at GracefulStop start) and the gRPC
// stream interceptor chain (consumer: StreamDrain binds each in-flight stream's
// context to Context()). grpc-go's GracefulStop only *waits* for in-flight
// streams — it never cancels their handler contexts — so a long-lived
// server-stream that ignores shutdown blocks until the hard-stop deadline. The
// DrainSignal closes that gap: Trigger cancels Context(), every active stream's
// ctx.Done() fires, handlers that select on it return promptly, and GracefulStop
// completes within budget.
//
// The composition root constructs ONE DrainSignal and wires it symmetrically
// into both adaptersgrpc.Config.Drain and interceptor.Deps.Drain — the same
// Option-3 instance-sharing discipline as the shared ServiceRegistrar (#1152): a
// different instance on each side would let the producer trigger a signal no
// consumer observes. A compile-proof single-builder that emits the adapter
// config, both interceptor chains, the registrar, and this drain together is the
// Hard upgrade, tracked at #1752.
type DrainSignal struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewDrainSignal returns a fresh, un-triggered DrainSignal. The fields are
// unexported, so this is the only way to obtain a usable value — a zero-value
// DrainSignal{} cannot be constructed outside this package.
func NewDrainSignal() *DrainSignal {
	ctx, cancel := context.WithCancel(context.Background())
	return &DrainSignal{ctx: ctx, cancel: cancel}
}

// Context returns a context canceled (with context.Canceled) the first time
// Trigger is called. StreamDrain derives each stream's handler context from it
// so drain propagates as ordinary ctx cancellation. It is never nil for a value
// built by NewDrainSignal.
func (d *DrainSignal) Context() context.Context { return d.ctx }

// Trigger cancels Context(). It is idempotent and safe for concurrent use: the
// adapter calls it once at GracefulStop start, and any further call is a
// harmless no-op (context.CancelFunc is itself idempotent).
func (d *DrainSignal) Trigger() { d.cancel() }
