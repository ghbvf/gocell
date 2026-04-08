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
//
// On panic, recording is skipped because the inner middleware's code after
// ServeHTTP does not execute. Recovery logs the full panic context separately.
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

			collector.RecordRequest(r.Method, r.URL.Path, state.Status(), time.Since(start).Seconds())
		})
	}
}
