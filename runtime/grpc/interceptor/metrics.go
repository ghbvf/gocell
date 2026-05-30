package interceptor

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/observability"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// UnaryMetrics returns an interceptor that records grpc_server_requests_total
// and grpc_server_request_duration_seconds via the given collector. The metric
// labels are: method (info.FullMethod), code (the gRPC status code name derived
// from the handler's returned error), and cell. The cell label reads
// kernel/ctxkeys.CellID and falls back to RuntimeCellIDSentinel ("_runtime");
// gRPC cell attribution is not wired until a later PR, so cell is "_runtime"
// for now. This mirrors runtime/http/middleware.Metrics.
func UnaryMetrics(collector metrics.GRPCCollector, clk clock.Clock) grpc.UnaryServerInterceptor {
	clock.MustHaveClock(clk, "interceptor.UnaryMetrics")
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := clk.Now()
		resp, err := handler(ctx, req)
		observability.SafeObserve(slog.Default(), func() {
			code := status.Code(err).String()
			cellID := middleware.RuntimeCellIDSentinel
			if v, ok := kernelctxkeys.CellIDFrom(ctx); ok && v != "" {
				cellID = v
			}
			collector.RecordRPC(ctx, cellID, info.FullMethod, code, clk.Since(start).Seconds())
		})
		return resp, err
	}
}
