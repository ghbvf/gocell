package middleware

import (
	"log/slog"
	"runtime/debug"
)

// safeObserve runs fn and recovers from any panic, logging it via slog.Error
// instead of letting it escape the request chain.
//
// Used by observability middleware (AccessLog, Metrics) whose post-ServeHTTP
// code sits outside Recovery. A bug in the collector or logger must not crash
// the process — at worst it drops one observation.
//
// ref: prometheus/client_golang promhttp — instrumentation handler panics are
// silently dropped rather than propagated to the caller.
func safeObserve(fn func()) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("observability middleware panic",
				slog.Any("panic", v),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	fn()
}
