package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// AccessLog logs structured request/response information via slog.Info.
// Fields: method, path, status, duration_ms, request_id.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := NewRecorder(w)

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		attrs := []any{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.Status()),
			slog.Int64("duration_ms", duration.Milliseconds()),
		}
		if reqID, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
			attrs = append(attrs, slog.String("request_id", reqID))
		}
		slog.Info("http request", attrs...)
	})
}
