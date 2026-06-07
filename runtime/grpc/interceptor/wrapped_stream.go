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
// wrapped it), its context is replaced in place so chained interceptors don't
// nest wrappers; otherwise a fresh wrapper is allocated. Either way the next
// interceptor / handler observes ctx, which incorporates every prior mutation.
func wrapServerStream(ss grpc.ServerStream, ctx context.Context) grpc.ServerStream {
	if existing, ok := ss.(*wrappedServerStream); ok {
		existing.ctx = ctx
		return existing
	}
	return &wrappedServerStream{ServerStream: ss, ctx: ctx}
}
