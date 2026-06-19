package interceptor

// rate_limit.go — per-key gRPC rate-limit interceptors (unary + stream).
//
// The RateLimiter interface is declared here with a narrow footprint (Allow(key)
// bool) that is structurally compatible with the HTTP middleware's ratelimit.Limiter
// interface but WITHOUT a direct import dependency. The coupling is intentional:
// gRPC and HTTP rate-limiters share deployment (e.g. the same redis token-bucket
// adapter), but the interceptor package must not import runtime/http/middleware.
//
// When RateLimiter is nil the interceptor is a transparent pass-through — this
// is an opt-in, per-listener protection feature. A nil limiter silently does
// nothing (business reason documented in the no-op comment below).
//
// Key extraction: the rate-limit key is the peer IP address (port stripped via
// net.SplitHostPort). This matches the HTTP middleware's X-Real-IP strategy
// but uses the gRPC peer context directly. If no peer is present (e.g. local
// in-process call, unit test without peer), the key is "" — the limiter still
// runs and may or may not restrict anonymous callers, depending on its policy.
//
// ref: golang.org/x/time/rate TokenBucket (rate.Limiter)
// ref: go-zero/core/limit TokenLimiter

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RateLimiter is the narrow interface for per-key rate limiting.
// It is structurally compatible with the HTTP middleware's ratelimit.Limiter
// interface (Allow(key string) bool) but declared here to avoid coupling to
// runtime/http/middleware.
type RateLimiter interface {
	// Allow reports whether the request identified by key should be allowed.
	// The key is the caller's IP address (port stripped). key may be "" for
	// in-process callers without a peer context.
	Allow(key string) bool
}

// peerKey extracts the rate-limit key from the gRPC peer context. The key is
// the peer's IP address with the port stripped via net.SplitHostPort. Two edge
// cases:
//   - No peer in context (in-process call / unit test without peer): returns "".
//   - Peer present but address unparseable (e.g. unix-socket path): returns the
//     raw Addr().String() so each distinct address keeps its OWN bucket rather
//     than collapsing into the shared "" anonymous bucket (no rate-limit bypass).
//
// Note: when the limiter is non-nil and the peer is unavailable (in-process
// call, unix socket, or unit test without peer injection), all such calls
// collapse to the "" bucket. Deployers enabling a limiter on a listener that
// also carries in-process or unix-socket traffic should configure a lenient
// policy for the empty key to avoid starving legitimate in-process callers.
func peerKey(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		// Unparseable peer address (e.g. unix socket path): use raw string.
		return p.Addr.String()
	}
	return host
}

// UnaryRateLimit returns a unary server interceptor that rate-limits by peer IP.
// When limiter is nil the interceptor is a transparent pass-through (opt-in
// protection: deployers that have not configured a rate-limiter should not be
// penalized).
func UnaryRateLimit(limiter RateLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if limiter == nil {
			// nil limiter: opt-in protection not configured; pass through.
			return handler(ctx, req)
		}
		key := peerKey(ctx)
		if !limiter.Allow(key) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// StreamRateLimit returns a stream server interceptor that rate-limits by peer IP.
// Semantics mirror UnaryRateLimit: nil limiter is a pass-through.
//
// Note: this interceptor gates at stream ESTABLISHMENT only — one Allow() call
// per stream open. Per-message flow control (allowing/denying individual messages
// within an established stream) is out of scope and tracked separately.
func StreamRateLimit(limiter RateLimiter) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if limiter == nil {
			// nil limiter: opt-in protection not configured; pass through.
			return handler(srv, ss)
		}
		key := peerKey(ss.Context())
		if !limiter.Allow(key) {
			return status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}
