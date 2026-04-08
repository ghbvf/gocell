package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Tracing creates an HTTP middleware that starts a span for each request.
// The span name is "{method} {path}". Trace and span IDs are stored in the
// request context via ctxkeys for logging correlation.
//
// If a RecorderState already exists in the context (set by Recovery),
// Tracing reuses it to avoid additional httpsnoop wrapping.
//
// On panic, the span is ended (via defer) but status is not recorded because
// the inner defer runs before Recovery catches the panic. Recovery logs the
// full panic context separately.
func Tracing(tracer tracing.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spanName := r.Method + " " + r.URL.Path
			ctx, span := tracer.Start(r.Context(), spanName)
			defer span.End()

			state := RecorderStateFrom(ctx)
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
			}

			next.ServeHTTP(w, r.WithContext(ctx))

			span.SetAttribute("http.status_code", state.Status())
		})
	}
}
