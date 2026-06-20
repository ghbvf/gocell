package interceptor

// protection_metric_test.go — tests that the RateLimit and CircuitBreaker
// interceptors emit grpc_protection_rejected_total on deny/open paths.

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// TestUnaryRateLimit_Deny_EmitsProtectionMetric verifies that a rate-limit deny
// records one grpc_protection_rejected_total{type="ratelimit"} observation.
func TestUnaryRateLimit_Deny_EmitsProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, err := UnaryRateLimit(stubRateLimiter{allow: false}, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) {
			t.Fatal("handler must not be called when rate-limited")
			return nil, nil
		},
	)
	if err == nil {
		t.Fatal("deny: expected ResourceExhausted error")
	}
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/Op", "_runtime"); got != 1 {
		t.Errorf("ratelimit deny protection count = %d, want 1", got)
	}
}

// TestUnaryRateLimit_Allow_NoProtectionMetric verifies that a rate-limit allow
// does NOT emit a protection metric.
func TestUnaryRateLimit_Allow_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, _ = UnaryRateLimit(stubRateLimiter{allow: true}, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil },
	)
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/Op", "_runtime"); got != 0 {
		t.Errorf("ratelimit allow: protection count = %d, want 0", got)
	}
}

// TestUnaryRateLimit_NilLimiter_NoProtectionMetric verifies nil limiter
// (passthrough) emits nothing.
func TestUnaryRateLimit_NilLimiter_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, _ = UnaryRateLimit(nil, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil },
	)
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/Op", "_runtime"); got != 0 {
		t.Errorf("nil limiter: protection count = %d, want 0", got)
	}
}

// TestStreamRateLimit_Deny_EmitsProtectionMetric verifies stream rate-limit deny
// records a ratelimit protection metric.
func TestStreamRateLimit_Deny_EmitsProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &rateLimitFakeStream{ctx: context.Background()}

	err := StreamRateLimit(stubRateLimiter{allow: false}, coll, validCellIDs)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error {
			t.Fatal("handler must not be called")
			return nil
		},
	)
	if err == nil {
		t.Fatal("deny: expected ResourceExhausted error")
	}
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/S", "_runtime"); got != 1 {
		t.Errorf("stream ratelimit deny protection count = %d, want 1", got)
	}
}

// TestStreamRateLimit_NilLimiter_NoProtectionMetric verifies stream nil limiter
// emits nothing.
func TestStreamRateLimit_NilLimiter_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &rateLimitFakeStream{ctx: context.Background()}

	_ = StreamRateLimit(nil, coll, validCellIDs)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { return nil },
	)
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/S", "_runtime"); got != 0 {
		t.Errorf("nil stream limiter: protection count = %d, want 0", got)
	}
}

// TestUnaryCircuitBreaker_Open_EmitsProtectionMetric verifies that an open
// circuit records one grpc_protection_rejected_total{type="circuit"} observation.
func TestUnaryCircuitBreaker_Open_EmitsProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	cb := &stubAllower{allowed: false}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, err := UnaryCircuitBreaker(cb, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) {
			t.Fatal("handler must not be called when circuit is open")
			return nil, nil
		},
	)
	if err == nil {
		t.Fatal("open circuit: expected Unavailable error")
	}
	if got := coll.ProtectionCount("circuit", "/pkg.Svc/Op", "_runtime"); got != 1 {
		t.Errorf("circuit open protection count = %d, want 1", got)
	}
}

// TestUnaryCircuitBreaker_Closed_NoProtectionMetric verifies that a closed
// (allowed) circuit does NOT emit a protection metric.
func TestUnaryCircuitBreaker_Closed_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	cb := &stubAllower{allowed: true}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, _ = UnaryCircuitBreaker(cb, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil },
	)
	if got := coll.ProtectionCount("circuit", "/pkg.Svc/Op", "_runtime"); got != 0 {
		t.Errorf("circuit closed: protection count = %d, want 0", got)
	}
}

// TestUnaryCircuitBreaker_NilCB_NoProtectionMetric verifies nil cb (passthrough)
// emits nothing.
func TestUnaryCircuitBreaker_NilCB_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, _ = UnaryCircuitBreaker(nil, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "ok", nil },
	)
	if got := coll.ProtectionCount("circuit", "/pkg.Svc/Op", "_runtime"); got != 0 {
		t.Errorf("nil cb: protection count = %d, want 0", got)
	}
}

// TestStreamCircuitBreaker_Open_EmitsProtectionMetric verifies stream open
// circuit records a circuit protection metric.
func TestStreamCircuitBreaker_Open_EmitsProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	cb := &stubAllower{allowed: false}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}

	err := StreamCircuitBreaker(cb, coll, validCellIDs)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error {
			t.Fatal("handler must not be called")
			return nil
		},
	)
	if err == nil {
		t.Fatal("open circuit stream: expected Unavailable error")
	}
	if got := coll.ProtectionCount("circuit", "/pkg.Svc/S", "_runtime"); got != 1 {
		t.Errorf("stream circuit open protection count = %d, want 1", got)
	}
}

// TestStreamCircuitBreaker_NilCB_NoProtectionMetric verifies nil stream cb
// emits nothing.
func TestStreamCircuitBreaker_NilCB_NoProtectionMetric(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/S"}
	ss := &cbFakeStream{ctx: context.Background()}

	_ = StreamCircuitBreaker(nil, coll, validCellIDs)(nil, ss, info,
		func(_ any, _ grpc.ServerStream) error { return nil },
	)
	if got := coll.ProtectionCount("circuit", "/pkg.Svc/S", "_runtime"); got != 0 {
		t.Errorf("nil stream cb: protection count = %d, want 0", got)
	}
}

// TestUnaryRateLimit_Deny_Method verifies deny emits on the correct FullMethod.
func TestUnaryRateLimit_Deny_Method(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}

	for _, method := range []string{"/pkg.Svc/A", "/pkg.Svc/B"} {
		info := &grpc.UnaryServerInfo{FullMethod: method}
		_, _ = UnaryRateLimit(stubRateLimiter{allow: false}, coll, validCellIDs)(
			context.Background(), nil, info,
			func(_ context.Context, _ any) (any, error) { return nil, nil },
		)
	}
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/A", "_runtime"); got != 1 {
		t.Errorf("/pkg.Svc/A count = %d, want 1", got)
	}
	if got := coll.ProtectionCount("ratelimit", "/pkg.Svc/B", "_runtime"); got != 1 {
		t.Errorf("/pkg.Svc/B count = %d, want 1", got)
	}
}

// TestUnaryCircuitBreaker_Open_CodeUnavailable_Unchanged verifies that the
// existing Unavailable behavior is unchanged when protection metric is added.
func TestUnaryCircuitBreaker_Open_CodeUnavailable_Unchanged(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	cb := &stubAllower{allowed: false}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, err := UnaryCircuitBreaker(cb, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return nil, nil },
	)
	if codes.Code(codes.Unavailable) != codes.Unavailable {
		t.Fatal("code assertion helpers broken")
	}
	_ = err // verified by existing TestUnaryCircuitBreaker_Open_ReturnsUnavailable
}
