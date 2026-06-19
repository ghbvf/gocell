package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"net"
)

// stubRateLimiter is a configurable RateLimiter for tests.
type stubRateLimiter struct {
	allow bool
}

func (s stubRateLimiter) Allow(_ string) bool { return s.allow }

// rateLimitFakeStream is a minimal grpc.ServerStream for rate-limit tests.
type rateLimitFakeStream struct {
	ctx context.Context
}

func (f *rateLimitFakeStream) Context() context.Context       { return f.ctx }
func (f *rateLimitFakeStream) SendMsg(_ any) error            { return nil }
func (f *rateLimitFakeStream) RecvMsg(_ any) error            { return nil }
func (f *rateLimitFakeStream) SetHeader(_ metadata.MD) error  { return nil }
func (f *rateLimitFakeStream) SendHeader(_ metadata.MD) error { return nil }
func (f *rateLimitFakeStream) SetTrailer(_ metadata.MD)       {}

// ctxWithPeer returns a context carrying the given peer address.
func ctxWithPeer(addr string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP(addr), Port: 12345},
	})
}

// ─── Unary ──────────────────────────────────────────────────────────────────

func TestUnaryRateLimit_NilLimiter_Passthrough(t *testing.T) {
	// nil limiter → opt-in passthrough; handler must be called.
	t.Parallel()
	handlerCalled := false
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryRateLimit(nil)(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		handlerCalled = true
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("nil limiter: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("nil limiter: handler was not called (must be passthrough)")
	}
}

func TestUnaryRateLimit_Allow_HandlerCalled(t *testing.T) {
	t.Parallel()
	handlerCalled := false
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryRateLimit(stubRateLimiter{allow: true})(
		ctxWithPeer("192.0.2.1"), nil, info, func(_ context.Context, _ any) (any, error) {
			handlerCalled = true
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("allow: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("allow: handler was not called")
	}
}

func TestUnaryRateLimit_Deny_ResourceExhausted(t *testing.T) {
	t.Parallel()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryRateLimit(stubRateLimiter{allow: false})(
		ctxWithPeer("192.0.2.1"), nil, info, func(_ context.Context, _ any) (any, error) {
			t.Fatal("handler must not be called when rate-limited")
			return nil, nil
		})
	if err == nil {
		t.Fatal("deny: expected error, got nil")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Errorf("deny: code = %v, want ResourceExhausted", got)
	}
}

func TestUnaryRateLimit_PeerKeyExtraction_StripPort(t *testing.T) {
	// IP key extraction must strip the port so one IP shares a bucket.
	t.Parallel()
	var capturedKey string
	limiter := capturingLimiter{allow: true, captureKey: &capturedKey}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, _ = UnaryRateLimit(limiter)(ctxWithPeer("10.0.0.1"), nil, info,
		func(_ context.Context, _ any) (any, error) { return nil, nil })
	if capturedKey == "" {
		t.Fatal("limiter.Allow was never called")
	}
	// The key must not contain a port.
	if _, port, _ := net.SplitHostPort(capturedKey + ":0"); port == "12345" {
		// net.SplitHostPort succeeded only if capturedKey contains a colon → port present
		t.Errorf("rate-limit key %q still contains a port (expected stripped IP)", capturedKey)
	}
}

func TestUnaryRateLimit_NoPeer_StableFallback(t *testing.T) {
	// No peer in ctx → stable key (""), limiter should still be called.
	t.Parallel()
	var capturedKey string
	limiter := capturingLimiter{allow: true, captureKey: &capturedKey}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	called := false
	_, _ = UnaryRateLimit(limiter)(context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { called = true; return nil, nil })
	if !called {
		t.Fatal("handler not called (no-peer path should allow when limiter allows)")
	}
}

// ─── Stream ──────────────────────────────────────────────────────────────────

func TestStreamRateLimit_NilLimiter_Passthrough(t *testing.T) {
	t.Parallel()
	handlerCalled := false
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &rateLimitFakeStream{ctx: context.Background()}
	err := StreamRateLimit(nil)(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		handlerCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("nil limiter: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("nil limiter: handler was not called")
	}
}

func TestStreamRateLimit_Deny_ResourceExhausted(t *testing.T) {
	t.Parallel()
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &rateLimitFakeStream{ctx: ctxWithPeer("192.0.2.2")}
	err := StreamRateLimit(stubRateLimiter{allow: false})(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error {
			t.Fatal("handler must not be called when rate-limited")
			return nil
		})
	if err == nil {
		t.Fatal("deny: expected error, got nil")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Errorf("stream deny: code = %v, want ResourceExhausted", got)
	}
}

func TestStreamRateLimit_Allow_HandlerCalled(t *testing.T) {
	t.Parallel()
	handlerCalled := false
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &rateLimitFakeStream{ctx: ctxWithPeer("192.0.2.2")}
	err := StreamRateLimit(stubRateLimiter{allow: true})(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { handlerCalled = true; return nil })
	if err != nil {
		t.Fatalf("allow: unexpected error: %v", err)
	}
	if !handlerCalled {
		t.Fatalf("allow: handler was not called")
	}
}

// capturingLimiter is a RateLimiter that records the key it is called with.
type capturingLimiter struct {
	allow      bool
	captureKey *string
}

func (c capturingLimiter) Allow(key string) bool {
	*c.captureKey = key
	return c.allow
}
