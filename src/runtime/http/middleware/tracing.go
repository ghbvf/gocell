package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Tracing creates an HTTP middleware that starts a span for each request.
// The span name is "{method} {path}". Trace and span IDs are stored in the
// request context via ctxkeys for logging correlation.
//
// Tracing expects a RecorderState to already exist in the request context,
// created by the Recorder middleware earlier in the chain.
func Tracing(tracer tracing.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spanName := r.Method + " " + r.URL.Path
			ctx, span := tracer.Start(r.Context(), spanName)
			defer span.End()

			state := RecorderStateFrom(ctx)

			next.ServeHTTP(w, r.WithContext(ctx))

			if state != nil {
				span.SetAttribute("http.status_code", state.Status())
			}
		})
	}
}
