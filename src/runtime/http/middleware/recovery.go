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
// When a RecorderState exists in the context (created by the Recorder
// middleware), Recovery reuses it so upstream middleware (AccessLog, Metrics)
// can observe the 500 status. When used standalone without Recorder,
// Recovery creates its own RecorderState and stores it in the context,
// preserving committed-response detection.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := RecorderStateFrom(r.Context())
		if state == nil {
			var wrapped http.ResponseWriter
			state, wrapped = NewRecorder(w)
			ctx := WithRecorderState(r.Context(), state)
			r = r.WithContext(ctx)
			w = wrapped
		}

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

				if state.Committed() {
					attrs = append(attrs, slog.Bool("response_committed", true))
					slog.Error("panic after response committed", attrs...)
					return
				}

				slog.Error("panic recovered", attrs...)
				httputil.WriteError(r.Context(), w, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
