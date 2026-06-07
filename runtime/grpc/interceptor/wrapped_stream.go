package interceptor

import (
	"context"

	"google.golang.org/grpc"
)

// wrapped_stream.go — grpc.ServerStream context wrapper for the streaming
// interceptor chain (GAP-1 PR-10 [#1153]).
//
// A grpc.StreamServerInterceptor cannot mutate the handler's context directly —
// the handler reads it from the ServerStream. wrappedServerStream overrides
// Context() so the ctx-mutating stream interceptors (StreamRequestID /
// StreamCellAttribution / StreamAuth / StreamDrain) can thread their derived
// context to the handler and to every inner interceptor.
//
// ref: grpc-ecosystem/go-grpc-middleware wrappers.go — WrappedServerStream

// wrappedServerStream embeds grpc.ServerStream (so all stream methods pass
// through unchanged) and overrides Context().
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the interceptor-derived context instead of the underlying
// stream's original one.
func (w *wrappedServerStream) Context() context.Context { return w.ctx }

// wrapServerStream returns a grpc.ServerStream whose Context() reports ctx. When
// ss is already a *wrappedServerStream (i.e. an outer interceptor already
// wrapped it), its context is **replaced in place** so chained interceptors
// don't nest wrappers; otherwise a fresh wrapper is allocated. Either way the
// next interceptor / handler observes ctx, which incorporates every prior
// mutation.
//
// WARNING — in-place mutation: because the wrapper is shared and reused down the
// chain, any code that captures the stream reference and reads ss.Context()
// AFTER passing it inward (e.g. an interceptor that reads it both before and
// after handler) will observe the inner-most ctx, not the one it passed. This is
// safe in the current chain — every ctx is additive (each interceptor only adds
// request-id / cell-id / principal / drain-cancel), so the inner-most ctx is a
// superset, and the observability interceptors read ss.Context() only after the
// handler returns. A future interceptor that needs a stable pre-handler snapshot
// MUST bind ctx := ss.Context() into a local before calling handler.
func wrapServerStream(ss grpc.ServerStream, ctx context.Context) grpc.ServerStream {
	if existing, ok := ss.(*wrappedServerStream); ok {
		existing.ctx = ctx
		return existing
	}
	return &wrappedServerStream{ServerStream: ss, ctx: ctx}
}
