package middleware

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector.
//
// Metrics expects a RecorderState to already exist in the request context,
// created by the Recorder middleware earlier in the chain. This ensures
// panic requests (caught by Recovery downstream) are recorded with
// status 500 rather than being invisible.
func Metrics(collector metrics.Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			state := RecorderStateFrom(r.Context())

			next.ServeHTTP(w, r)

			collector.RecordRequest(r.Method, r.URL.Path, state.Status(), time.Since(start).Seconds())
		})
	}
}
