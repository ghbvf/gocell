package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestUnaryMetrics(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	tests := []struct {
		name     string
		handler  grpc.UnaryHandler
		wantCode string
	}{
		{
			name:     "success records OK",
			handler:  func(context.Context, any) (any, error) { return "ok", nil },
			wantCode: codes.OK.String(),
		},
		{
			name:     "status error records its code",
			handler:  func(context.Context, any) (any, error) { return nil, status.Error(codes.NotFound, "x") },
			wantCode: codes.NotFound.String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coll := metrics.NewInMemoryGRPCCollector()
			_, _ = UnaryMetrics(coll, clock.Real())(context.Background(), nil, info, tt.handler)
			// cell defaults to the runtime sentinel until gRPC cell attribution
			// is wired.
			if got := coll.Count(metrics.RuntimeCellSentinel, method, tt.wantCode); got != 1 {
				t.Fatalf("count[%s] = %d, want 1", tt.wantCode, got)
			}
		})
	}

	t.Run("cell label degrades to sentinel when gRPC attribution not yet wired", func(t *testing.T) {
		// UnaryMetrics passes nil for the validCellIDs closed set because gRPC
		// cell attribution is not yet wired (#1383). ResolveCellLabel(ctx, nil)
		// always degrades to RuntimeCellSentinel regardless of what is in ctx.
		// When attribution lands (nil → assembly closed set), this subtest should
		// be updated to assert the owning cell is reflected instead.
		coll := metrics.NewInMemoryGRPCCollector()
		ctx := kernelctxkeys.WithCellID(context.Background(), "mycell")
		_, _ = UnaryMetrics(coll, clock.Real())(ctx, nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if got := coll.Count(metrics.RuntimeCellSentinel, method, codes.OK.String()); got != 1 {
			t.Fatalf("count[_runtime] = %d, want 1 (gRPC attribution not yet wired → sentinel)", got)
		}
		if got := coll.Count("mycell", method, codes.OK.String()); got != 0 {
			t.Fatalf("count[mycell] = %d, want 0 (cell attribution not yet active)", got)
		}
	})
}

func TestUnaryMetricsNilCollectorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("UnaryMetrics with nil collector must panic at construction")
		}
	}()
	_ = UnaryMetrics(nil, clock.Real())
}
