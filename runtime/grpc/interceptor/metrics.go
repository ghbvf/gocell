package interceptor

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/observability"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// UnaryMetrics returns an interceptor that records grpc_server_requests_total
// and grpc_server_request_duration_seconds via the given collector. The metric
// labels are: method (info.FullMethod), code (the gRPC status code name derived
// from the handler's returned error), and cell. The cell label is resolved
// through the sealed metrics.ResolveCellLabel funnel against validCellIDs (the
// assembly cell-id set): the cell written by UnaryCellAttribution (which runs
// outer to this interceptor, #1152) is reflected when it is a member, and an
// absent or out-of-set cell degrades to metrics.RuntimeCellSentinel ("_runtime").
// This mirrors runtime/http/middleware.Metrics.
//
// collector and clk are required: a nil collector or clock is a wiring bug that
// fails fast at construction (programmer-error panic, like MustHaveClock) rather
// than nil-dereferencing on the first RPC where UnaryRecovery would mask it as a
// generic codes.Internal.
func UnaryMetrics(collector metrics.GRPCCollector, clk clock.Clock, validCellIDs map[string]struct{}) grpc.UnaryServerInterceptor {
	clock.MustHaveClock(clk, "interceptor.UnaryMetrics")
	if validation.IsNilInterface(collector) {
		panic(panicregister.Approved("interceptor-metrics-collector-required",
			errcode.Assertion("interceptor.UnaryMetrics: collector is required")))
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := clk.Now()
		resp, err := handler(ctx, req)
		observability.SafeObserve(slog.Default(), func() {
			code := status.Code(err).String()
			cell := metrics.ResolveCellLabel(ctx, validCellIDs)
			collector.RecordRPC(ctx, cell, info.FullMethod, code, clk.Since(start).Seconds())
		})
		return resp, err
	}
}
