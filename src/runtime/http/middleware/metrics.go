package middleware

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Metrics returns an HTTP middleware that records request count and duration
// using the provided Collector.
func Metrics(collector metrics.Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := NewRecorder(w)

			next.ServeHTTP(rec, r)

			duration := time.Since(start).Seconds()
			collector.RecordRequest(r.Method, r.URL.Path, rec.Status(), duration)
		})
	}
}
