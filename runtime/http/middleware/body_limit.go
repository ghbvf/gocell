package middleware

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/observability"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// DefaultBodyLimit is the default maximum request body size (1 MB).
const DefaultBodyLimit int64 = 1 << 20

// BodyLimit restricts the request body to at most maxBytes bytes.
// If the Content-Length header exceeds the limit, a 413 JSON error response is
// returned immediately (fast-path) and the rejection is recorded via collector
// when collector is non-nil. Streaming overruns after MaxBytesReader kicks in
// are captured by http_requests_total{status=413} through RecordRequest.
// Pass 0 or a negative value to use DefaultBodyLimit.
//
// collector=nil disables only the body-limit fast-path rejection counter
// (RecordBodyLimitRejection); it does not affect streaming 413 accounting.
// Streaming overruns are independently recorded by the Metrics middleware via
// RecordRequest and remain observable regardless of the collector value here.
func BodyLimit(maxBytes int64, collector metrics.Collector) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultBodyLimit
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				recordBodyLimitRejection(collector, r)
				writeBodyTooLarge(r.Context(), w)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// recordBodyLimitRejection extracts cell and route from the request context and
// calls collector.RecordBodyLimitRejection. It is a no-op when collector is nil.
// Complexity extracted here to keep BodyLimit's cognitive complexity ≤ 15.
func recordBodyLimitRejection(collector metrics.Collector, r *http.Request) {
	if collector == nil {
		return
	}
	ctx := r.Context()
	cellID := RuntimeCellIDSentinel
	if v, ok := ctxkeys.CellIDFrom(ctx); ok && v != "" {
		cellID = v
	}
	route := RouteFor(ctx, r.Method, r.URL.Path)
	observability.SafeObserve(slog.Default(), func() {
		collector.RecordBodyLimitRejection(ctx, cellID, route)
	})
}

func writeBodyTooLarge(ctx context.Context, w http.ResponseWriter) {
	httputil.WriteError(ctx, w,
		errcode.New(errcode.KindPayloadTooLarge, errcode.ErrBodyTooLarge, "request body too large"))
}
