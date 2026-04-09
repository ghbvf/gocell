package middleware

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector.
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), Metrics reuses it. Otherwise it creates its own to
// remain usable as a standalone middleware.
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

			next.ServeHTTP(w, r)

			safeObserve(func() {
				collector.RecordRequest(r.Method, r.URL.Path, state.Status(), time.Since(start).Seconds())
			})
		})
	}
}
