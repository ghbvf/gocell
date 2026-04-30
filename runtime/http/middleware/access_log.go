package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// AccessLog logs structured request/response information via slog.Info.
// Fields: method, path, route, status, duration_ms, listener, request_id,
// correlation_id, trace_id, real_ip. The listener field is emitted only when
// the router annotated the request with a non-empty physical listener name.
//
// ref: go-zero rest/handler/loghandler.go — structured request logging with trace context
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), AccessLog reuses it. Otherwise it creates its own to
// remain usable as a standalone middleware.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		state, rw := accessLogRecorder(w, r)

		next.ServeHTTP(rw, r)
		logAccessRequest(start, r, state)
	})
}

func accessLogRecorder(w http.ResponseWriter, r *http.Request) (*RecorderState, http.ResponseWriter) {
	state := RecorderStateFrom(r.Context())
	if state != nil {
		return state, w
	}
	state, wrapped := NewRecorder(w)
	return state, wrapped
}

func logAccessRequest(start time.Time, r *http.Request, state *RecorderState) {
	safeObserve(slog.Default(), func() {
		slog.Info("http request", accessLogAttrs(start, r, state)...) //nolint:gosec // G706: structured slog attrs, not string concatenation
	})
}

func accessLogAttrs(start time.Time, r *http.Request, state *RecorderState) []any {
	duration := time.Since(start)
	attrs := []any{
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("route", RoutePatternFromCtx(r.Context())),
		slog.Int("status", state.Status()),
		slog.Int64("duration_ms", duration.Milliseconds()),
	}
	return appendAccessLogContextAttrs(attrs, r.Context())
}

func appendAccessLogContextAttrs(attrs []any, ctx context.Context) []any {
	attrs = appendAccessLogAttrFrom(ctx, attrs, "listener", listenerFromContext)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "request_id", ctxkeys.RequestIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "correlation_id", ctxkeys.CorrelationIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "trace_id", ctxkeys.TraceIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "real_ip", ctxkeys.RealIPFrom)
	return attrs
}

func appendAccessLogAttrFrom(
	ctx context.Context,
	attrs []any,
	key string,
	get func(context.Context) (string, bool),
) []any {
	value, ok := get(ctx)
	return appendAccessLogAttr(attrs, key, value, ok)
}

func appendAccessLogAttr(attrs []any, key, value string, ok bool) []any {
	if !ok {
		return attrs
	}
	return append(attrs, slog.String(key, value))
}
