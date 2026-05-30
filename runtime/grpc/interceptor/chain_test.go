package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func panicHandler(context.Context, any) (any, error) { panic("boom") }

// nestRecovered wraps a handler so that Recovery (innermost) runs closest to it.
func nestRecovered(info *grpc.UnaryServerInfo, h grpc.UnaryHandler) grpc.UnaryHandler {
	return func(ctx context.Context, req any) (any, error) {
		return UnaryRecovery()(ctx, req, info, h)
	}
}

// TestChainOrderRecoveryInnermost verifies the load-bearing ordering property:
// because Recovery is innermost, it converts a handler panic into
// codes.Internal *before* the outer Metrics and Tracing interceptors observe
// the result — so they record a clean Internal rather than a raw panic.
func TestChainOrderRecoveryInnermost(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	t.Run("metrics observes recovery-converted Internal", func(t *testing.T) {
		coll := metrics.NewInMemoryGRPCCollector()
		_, err := UnaryMetrics(coll, clock.Real())(
			context.Background(), nil, info, nestRecovered(info, panicHandler))
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		if got := coll.Count("_runtime", method, codes.Internal.String()); got != 1 {
			t.Fatalf("metrics Internal count = %d, want 1", got)
		}
	})

	t.Run("tracing observes recovery-converted error", func(t *testing.T) {
		tr := &recordingTracer{span: &recordingSpan{}}
		_, err := UnaryTracing(tr)(
			context.Background(), nil, info, nestRecovered(info, panicHandler))
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		if tr.span.recordedErr == nil {
			t.Fatalf("tracing did not record the recovery-converted error")
		}
	})
}

func TestNewUnaryChain(t *testing.T) {
	// Smoke: composition must not panic and must return a usable ServerOption.
	opt := NewUnaryChain(Deps{
		Collector: metrics.NewInMemoryGRPCCollector(),
		Clock:     clock.Real(),
		Verifier:  stubVerifier{},
	})
	if opt == nil {
		t.Fatalf("NewUnaryChain returned nil ServerOption")
	}
	// It must be installable on a real server without panicking.
	_ = grpc.NewServer(opt)
}
