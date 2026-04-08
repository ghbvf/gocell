package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Tracing creates an HTTP middleware that starts a span for each request.
// The span name is "{method} {path}". Trace and span IDs are stored in the
// request context via ctxkeys for logging correlation.
func Tracing(tracer tracing.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spanName := r.Method + " " + r.URL.Path
			ctx, span := tracer.Start(r.Context(), spanName)
			defer span.End()

			rec := NewRecorder(w)
			next.ServeHTTP(rec, r.WithContext(ctx))

			span.SetAttribute("http.status_code", rec.Status())
		})
	}
}
