package grpc

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

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
// The composition root constructs ONE DrainSignal inside interceptor.Deps; the
// adapter derives both the stream chain and graceful-stop trigger from that same
// deps object. This follows the same Option-3 instance-sharing discipline as the
// shared ServiceRegistrar (#1152): a missing signal is a composition bug and
// fails closed at startup.
type DrainSignal struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewDrainSignal returns a fresh, un-triggered DrainSignal. This is the ONLY way
// to obtain a usable value: the type is exported (it is a Config/Deps field), so
// a zero-value new(DrainSignal) / DrainSignal{} IS constructable outside this
// package, but its fields are nil — Trigger/Context would panic. Validate (called
// by adapters/grpc Config.validate and NewStreamChain) rejects such a value at
// startup.
func NewDrainSignal() *DrainSignal {
	ctx, cancel := context.WithCancel(context.Background())
	return &DrainSignal{ctx: ctx, cancel: cancel}
}

// Validate reports whether d is a usable signal built by NewDrainSignal. It is
// nil-receiver safe. A nil pointer or a zero-value (new(DrainSignal)) has a nil
// cancel/ctx and would panic at Trigger()/Context(); adapters/grpc Config.validate
// and NewStreamChain call Validate so a non-constructed signal fails fast at
// startup rather than at GracefulStop. The sealed-construction Hard upgrade
// (forbidding a zero-value at compile time) is tracked with #1752.
func (d *DrainSignal) Validate() error {
	if d == nil || d.cancel == nil || d.ctx == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"grpc: DrainSignal is not initialized; build it with runtimegrpc.NewDrainSignal()")
	}
	return nil
}

// Context returns a context canceled (with context.Canceled) the first time
// Trigger is called. StreamDrain derives each stream's handler context from it
// so drain propagates as ordinary ctx cancellation. It is never nil for a value
// built by NewDrainSignal (a zero-value is rejected by Validate before use).
func (d *DrainSignal) Context() context.Context { return d.ctx }

// Trigger cancels Context(). It is idempotent and safe for concurrent use: the
// adapter calls it once at GracefulStop start, and any further call is a
// harmless no-op (context.CancelFunc is itself idempotent).
func (d *DrainSignal) Trigger() { d.cancel() }
