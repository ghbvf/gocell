package interceptor

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// UnaryRecovery returns the innermost interceptor: it recovers panics raised by
// the handler, logs the redacted panic value plus stack via slog.Error, and
// converts the panic into a codes.Internal status returned to the client. It
// does NOT re-panic — the panic is collapsed into a return value so the outer
// Metrics and Tracing interceptors observe a clean codes.Internal. This mirrors
// runtime/http/middleware.Recovery (and the go-grpc-middleware / Kratos
// recovery convention); because it never calls panic() itself it is outside the
// PANIC-REGISTERED-01 funnel.
func UnaryRecovery() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if v := recover(); v != nil {
				stack := string(debug.Stack())
				// Sanitize the panic payload before logging — a raw value may
				// carry credentials or connection strings. RedactAny is the
				// sanctioned funnel for slog.Any("panic", ...).
				attrs := []any{
					slog.Any("panic", redaction.RedactAny(v)),
					slog.String("stack", stack),
					slog.String("method", info.FullMethod),
				}
				if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
					attrs = append(attrs, slog.String("request_id", reqID))
				}
				slog.ErrorContext(ctx, "panic recovered", attrs...)
				resp = nil
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
