package interceptor

// protection_metric_test.go — tests that the RateLimit and CircuitBreaker
// interceptors emit grpc_protection_rejected_total on deny/open paths.

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kernelctxkeys "github.com/ghbvf/gocell/framework/kernel/ctxkeys"
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
			return "unreachable", errors.New("unreachable")
		},
	)
	if err == nil {
		t.Fatal("deny: expected ResourceExhausted error")
	}
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "ratelimit"); got != 1 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "ratelimit"); got != 0 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "ratelimit"); got != 0 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/S", "ratelimit"); got != 1 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/S", "ratelimit"); got != 0 {
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
			return "unreachable", errors.New("unreachable")
		},
	)
	if err == nil {
		t.Fatal("open circuit: expected Unavailable error")
	}
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "circuit"); got != 1 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "circuit"); got != 0 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/Op", "circuit"); got != 0 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/S", "circuit"); got != 1 {
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
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/S", "circuit"); got != 0 {
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
			func(_ context.Context, _ any) (any, error) { return "unreachable", errors.New("unreachable") },
		)
	}
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/A", "ratelimit"); got != 1 {
		t.Errorf("/pkg.Svc/A count = %d, want 1", got)
	}
	if got := coll.ProtectionCount("_runtime", "/pkg.Svc/B", "ratelimit"); got != 1 {
		t.Errorf("/pkg.Svc/B count = %d, want 1", got)
	}
}

// TestUnaryCircuitBreaker_Open_CodeUnavailable_Unchanged verifies that the
// interceptor still returns codes.Unavailable after protection metric emission
// was added (F3: replaces the dead assertion codes.Code(0)==codes.Unavailable
// which was always false).
func TestUnaryCircuitBreaker_Open_CodeUnavailable_Unchanged(t *testing.T) {
	t.Parallel()
	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{}
	cb := &stubAllower{allowed: false}
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}

	_, err := UnaryCircuitBreaker(cb, coll, validCellIDs)(
		context.Background(), nil, info,
		func(_ context.Context, _ any) (any, error) { return "unreachable", errors.New("unreachable") },
	)
	// Verify the interceptor still returns Unavailable after we added metric emission.
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("open circuit: want codes.Unavailable, got %v (err=%v)", status.Code(err), err)
	}
}

// TestUnaryRateLimit_Deny_CellHit_EmitsWithCellLabel verifies that when a
// request carries a valid cell ID in context (in-set), the protection metric is
// emitted with that cell label rather than _runtime.
// F13: cell-hit path coverage.
func TestUnaryRateLimit_Deny_CellHit_EmitsWithCellLabel(t *testing.T) {
	t.Parallel()
	const cellName = "accesscore"
	const method = "/pkg.Svc/Op"

	coll := metrics.NewInMemoryGRPCCollector()
	validCellIDs := map[string]struct{}{cellName: {}}
	info := &grpc.UnaryServerInfo{FullMethod: method}
	ctx := kernelctxkeys.WithCellID(context.Background(), cellName)

	_, err := UnaryRateLimit(stubRateLimiter{allow: false}, coll, validCellIDs)(
		ctx, nil, info,
		func(_ context.Context, _ any) (any, error) {
			t.Fatal("handler must not be called when rate-limited")
			return "unreachable", errors.New("unreachable")
		},
	)
	if err == nil {
		t.Fatal("deny: expected ResourceExhausted error")
	}
	// Cell-hit: metric recorded under "accesscore", NOT "_runtime".
	if got := coll.ProtectionCount(cellName, method, "ratelimit"); got != 1 {
		t.Errorf("cell-hit ratelimit deny count for %q = %d, want 1", cellName, got)
	}
	// No bleed into _runtime sentinel.
	if got := coll.ProtectionCount("_runtime", method, "ratelimit"); got != 0 {
		t.Errorf("_runtime count must be 0 for in-set cell, got %d", got)
	}
}
