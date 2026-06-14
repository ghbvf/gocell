package middleware

import "net/http"

// Recorder creates a shared RecorderState and stores it in the request
// context so downstream middleware (AccessLog, Metrics, Recovery) can
// reuse it without redundant httpsnoop wrapping.
//
// Place Recorder early in the middleware chain — before any middleware
// that needs to observe the final response status (e.g. AccessLog,
// Metrics) and before Recovery so that a panic-recovered 500 response
// is visible to all observers.
func Recorder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, wrapped := NewRecorder(w)
		ctx := WithRecorderState(r.Context(), state)
		next.ServeHTTP(wrapped, r.WithContext(ctx))
	})
}
