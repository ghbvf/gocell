package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// ref: zeromicro/go-zero rest/handler/recoverhandler.go — RecoverHandler pattern
// Adopted: defer/recover with stack trace logging.
// Deviated: returns structured JSON error body per GoCell error-handling spec;
// uses slog instead of go-zero's internal logger.

// Recovery catches panics in downstream handlers, logs the panic value and
// stack trace via slog.Error, and returns a 500 JSON error response.
// If the response has already been committed (WriteHeader called), Recovery
// only logs the panic and does not attempt to write an error response.
//
// Recovery expects a RecorderState to already exist in the request context,
// created by the Recorder middleware earlier in the chain. This allows
// upstream middleware (AccessLog, Metrics) to observe the 500 status that
// Recovery writes after catching a panic.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := RecorderStateFrom(r.Context())

		defer func() {
			if v := recover(); v != nil {
				stack := string(debug.Stack())
				attrs := []any{
					slog.Any("panic", v),
					slog.String("stack", stack),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
				}
				if reqID, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
					attrs = append(attrs, slog.String("request_id", reqID))
				}

				if state != nil && state.Committed() {
					attrs = append(attrs, slog.Bool("response_committed", true))
					slog.Error("panic after response committed", attrs...)
					return
				}

				slog.Error("panic recovered", attrs...)
				httputil.WriteError(w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
