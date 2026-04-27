package middleware

import (
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

		state := RecorderStateFrom(r.Context())
		if state == nil {
			var wrapped http.ResponseWriter
			state, wrapped = NewRecorder(w)
			w = wrapped
		}

		next.ServeHTTP(w, r)

		safeObserve(slog.Default(), func() {
			duration := time.Since(start)
			route := RoutePatternFromCtx(r.Context())
			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", state.Status()),
				slog.Int64("duration_ms", duration.Milliseconds()),
			}
			if listener, ok := listenerFromContext(r.Context()); ok {
				attrs = append(attrs, slog.String("listener", listener))
			}
			if reqID, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
				attrs = append(attrs, slog.String("request_id", reqID))
			}
			if correlationID, ok := ctxkeys.CorrelationIDFrom(r.Context()); ok {
				attrs = append(attrs, slog.String("correlation_id", correlationID))
			}
			if traceID, ok := ctxkeys.TraceIDFrom(r.Context()); ok {
				attrs = append(attrs, slog.String("trace_id", traceID))
			}
			if realIP, ok := ctxkeys.RealIPFrom(r.Context()); ok {
				attrs = append(attrs, slog.String("real_ip", realIP))
			}
			slog.Info("http request", attrs...)
		})
	})
}
