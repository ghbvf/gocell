package middleware

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector.
//
// If a RecorderState already exists in the context (set by Recovery),
// Metrics reuses it to avoid additional httpsnoop wrapping.
// RecordRequest is called in a defer so that metrics are recorded even when
// a panic is caught by an outer Recovery middleware.
func Metrics(collector metrics.Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			state := RecorderStateFrom(r.Context())
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
			}

			defer func() {
				collector.RecordRequest(r.Method, r.URL.Path, state.Status(), time.Since(start).Seconds())
			}()

			next.ServeHTTP(w, r)
		})
	}
}
