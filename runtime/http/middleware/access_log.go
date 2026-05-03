package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	kctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	pkgctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/logutil"
)

// AccessLog returns an HTTP middleware that logs structured request/response
// information via slog.Info. A clock must be provided; use clock.Real() at the
// composition root.
// Fields: method, path, route, status, duration_ms, listener, cell_id,
// request_id, correlation_id, trace_id, real_ip. The listener field is emitted only when
// the router annotated the request with a non-empty physical listener name.
//
// ref: go-zero rest/handler/loghandler.go — structured request logging with trace context
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), AccessLog reuses it. Otherwise it creates its own to
// remain usable as a standalone middleware.
func AccessLog(clk clock.Clock) func(http.Handler) http.Handler {
	return accessLogWithClock(clk)
}

// accessLogWithClock is the clock-injectable variant used by AccessLog and tests.
func accessLogWithClock(clk clock.Clock) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "middleware.AccessLog")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := clk.Now()

			state, rw := accessLogRecorder(w, r)

			next.ServeHTTP(rw, r)
			logAccessRequest(start, r, state, clk)
		})
	}
}

func accessLogRecorder(w http.ResponseWriter, r *http.Request) (*RecorderState, http.ResponseWriter) {
	state := RecorderStateFrom(r.Context())
	if state != nil {
		return state, w
	}
	state, wrapped := NewRecorder(w)
	return state, wrapped
}

func logAccessRequest(start time.Time, r *http.Request, state *RecorderState, clk clock.Clock) {
	// Extract and sanitize request fields before logging to avoid taint flow
	// from user-controlled request data into structured log calls.
	method := logutil.Sanitize(r.Method)
	path := logutil.Sanitize(r.URL.Path)
	route := RoutePatternFromCtx(r.Context())
	ctx := r.Context()
	safeObserve(slog.Default(), func() {
		slog.Info("http request", accessLogAttrs(start, method, path, route, state, ctx, clk)...)
	})
}

func accessLogAttrs(start time.Time, method, path, route string, state *RecorderState, ctx context.Context, clk clock.Clock) []any {
	duration := clk.Since(start)
	attrs := []any{
		slog.String("method", method),
		slog.String("path", path),
		slog.String("route", route),
		slog.Int("status", state.Status()),
		slog.Int64("duration_ms", duration.Milliseconds()),
	}
	return appendAccessLogContextAttrs(attrs, ctx)
}

func appendAccessLogContextAttrs(attrs []any, ctx context.Context) []any {
	attrs = appendAccessLogAttrFrom(ctx, attrs, "listener", listenerFromContext)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "cell_id", kctxkeys.CellIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "request_id", pkgctxkeys.RequestIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "correlation_id", pkgctxkeys.CorrelationIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "trace_id", pkgctxkeys.TraceIDFrom)
	attrs = appendAccessLogAttrFrom(ctx, attrs, "real_ip", pkgctxkeys.RealIPFrom)
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
