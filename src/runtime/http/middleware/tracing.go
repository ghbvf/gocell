package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Tracing creates an HTTP middleware that starts a span for each request.
// The span is initially named "{method} {path}" and renamed to
// "{method} {routePattern}" after routing completes (if the span supports
// SpanRenamer). The http.route attribute carries the low-cardinality route
// pattern for OTel semantic conventions compliance.
//
// Span status follows the otelhttp convention: 5xx responses mark the span
// as error with the status text as description; 4xx and below leave the
// span status unset (the status code attribute is always recorded).
//
// ref: otelchi — extracts chi RoutePattern for span name after routing
// ref: otelhttp handler.go — span status set for 5xx, unset for 4xx
// ref: OTel semantic conventions — http.route must be low-cardinality template
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), Tracing reuses it. Otherwise it creates its own to
// capture http.status_code as a standalone middleware.
func Tracing(tracer tracing.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Start span with tentative name using raw path.
			// After routing, the span is renamed to use the route pattern.
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path)
			defer span.End()

			state := RecorderStateFrom(ctx)
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
			}

			next.ServeHTTP(w, r.WithContext(ctx))

			// After routing, use low-cardinality route pattern.
			route := RoutePatternFromCtx(r.Context())
			tracing.SpanSetName(span, r.Method+" "+route)
			span.SetAttribute("http.route", route)

			status := state.Status()
			span.SetAttribute("http.status_code", status)

			// 5xx → error span; 4xx and below → unset (otelhttp convention).
			if status >= 500 {
				tracing.SpanSetStatus(span, true, http.StatusText(status))
			}
		})
	}
}
