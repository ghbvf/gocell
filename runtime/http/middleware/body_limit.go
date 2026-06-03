package middleware

import (
	"context"
	"log/slog"
	"net/http"

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
// when collector is non-nil.
//
// Streaming overruns (MaxBytesReader kick-in after the body starts being read)
// are handler-dependent: the handler receives *http.MaxBytesError when reading
// the body. Framework-generated handlers propagate this error through
// pkg/httputil.WriteError, which maps it to a 413 response captured in
// http_requests_total{status="413"} via the Metrics middleware's RecordRequest.
// Custom handlers that do not call httputil.WriteError will not produce a 413
// in http_requests_total.
//
// Pass 0 or a negative value to use DefaultBodyLimit.
//
// collector=nil disables only the body-limit fast-path rejection counter
// (RecordBodyLimitRejection); it does not affect streaming 413 accounting.
//
// validCellIDs is the assembly's closed cell-id set (M12b #1093), threaded in by
// the router; the rejection's cell label is resolved through the sealed
// metrics.ResolveCellLabel funnel, so an out-of-set cell degrades to the
// RuntimeCellSentinel exactly like the Metrics middleware.
func BodyLimit(maxBytes int64, collector metrics.Collector, validCellIDs map[string]struct{}) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultBodyLimit
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				recordBodyLimitRejection(collector, r, validCellIDs)
				writeBodyTooLarge(r.Context(), w)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// recordBodyLimitRejection resolves the cell label through the sealed
// metrics.ResolveCellLabel funnel and calls collector.RecordBodyLimitRejection.
// It is a no-op when collector is nil. Complexity extracted here to keep
// BodyLimit's cognitive complexity ≤ 15.
func recordBodyLimitRejection(collector metrics.Collector, r *http.Request, validCellIDs map[string]struct{}) {
	if collector == nil {
		return
	}
	ctx := r.Context()
	cell := metrics.ResolveCellLabel(ctx, validCellIDs)
	route := RouteFor(ctx, r.Method, r.URL.Path)
	observability.SafeObserve(slog.Default(), func() {
		collector.RecordBodyLimitRejection(ctx, cell, route)
	})
}

func writeBodyTooLarge(ctx context.Context, w http.ResponseWriter) {
	httputil.WriteError(ctx, w,
		errcode.New(errcode.KindPayloadTooLarge, errcode.ErrBodyTooLarge, "request body too large"))
}
