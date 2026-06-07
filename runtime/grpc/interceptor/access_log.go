package interceptor

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	pkgctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/logutil"
	"github.com/ghbvf/gocell/pkg/observability"
)

// UnaryAccessLog returns an interceptor that logs one structured slog.Info line
// per RPC, mirroring the HTTP access-log middleware (runtime/http/middleware/
// access_log.go). It carries the HTTP-parity field set:
//
//	method, code, duration_ms, cell_id, request_id, correlation_id, trace_id
//
// Placement (chain order GRPC-INTERCEPTOR-CHAIN-ORDER-01): after RequestID +
// CellAttribution + Tracing (so request_id / cell_id / trace_id are already in
// ctx) and OUTER to Auth (so auth rejections are logged too). The auth Principal
// is deliberately NOT logged: UnaryAuth runs inner to this interceptor, so no
// auth.Principal is in ctx at log time — emitting it would be permanently dead
// code. Revisit only if auth ordering changes (e.g. device-token /
// per-method-public). trace_id is best-effort: UnaryTracing writes it only for a
// propagated/remote trace, same as the HTTP path. real_ip (an HTTP access-log
// field) is intentionally absent: gRPC has no X-Real-IP equivalent in this stack.
//
// Redaction is handled fail-closed at the slog sink (SLOG-HANDLER-SEALED-FUNNEL-01);
// like the HTTP access log, this interceptor does not redact field-side.
//
// clk is required (clock.MustHaveClock), matching the HTTP AccessLog contract.
func UnaryAccessLog(clk clock.Clock) grpc.UnaryServerInterceptor {
	clock.MustHaveClock(clk, "interceptor.UnaryAccessLog")
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := clk.Now()
		resp, err := handler(ctx, req)
		observability.SafeObserve(slog.Default(), func() {
			slog.Info("grpc request", accessLogAttrs(start, info.FullMethod, err, ctx, clk)...)
		})
		return resp, err
	}
}

// accessLogAttrs assembles the structured fields. fullMethod is sanitized to
// strip control characters before it reaches the log sink (defense in depth: the
// client controls the wire :path), mirroring logutil.Sanitize on the HTTP path.
func accessLogAttrs(start time.Time, fullMethod string, err error, ctx context.Context, clk clock.Clock) []any {
	attrs := []any{
		slog.String("method", logutil.Sanitize(fullMethod)),
		slog.String("code", status.Code(err).String()),
		slog.Int64("duration_ms", clk.Since(start).Milliseconds()),
	}
	attrs = appendCtxAttr(ctx, attrs, "cell_id", kernelctxkeys.CellIDFrom)
	attrs = appendCtxAttr(ctx, attrs, "request_id", pkgctxkeys.RequestIDFrom)
	attrs = appendCtxAttr(ctx, attrs, "correlation_id", pkgctxkeys.CorrelationIDFrom)
	attrs = appendCtxAttr(ctx, attrs, "trace_id", pkgctxkeys.TraceIDFrom)
	return attrs
}

// appendCtxAttr appends a string slog attr only when the getter reports present.
func appendCtxAttr(ctx context.Context, attrs []any, key string, get func(context.Context) (string, bool)) []any {
	if v, ok := get(ctx); ok {
		attrs = append(attrs, slog.String(key, v))
	}
	return attrs
}
