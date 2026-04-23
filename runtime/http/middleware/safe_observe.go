package middleware

import (
	"log/slog"
	"runtime/debug"
)

// safeObserve runs fn and recovers from any panic, logging it via the
// provided logger instead of letting it escape the request chain.
//
// The explicit *slog.Logger parameter (rather than slog.Default()) lets
// test code inject a broken handler to verify panic containment under
// logger failure. In production, callers pass their own middleware
// logger — typically derived from the composition root.
//
// Double-layer recover: the outer defer catches fn panics; the inner defer
// inside the log call catches panics from the logger itself. This ensures
// that a broken slog.Handler (e.g. one that panics in Handle) cannot escape
// safeObserve and kill the request-serving goroutine.
//
// Design note: Callers in production currently pass slog.Default() at the
// call site. This is an explicit choice — it lets test code inject a broken
// logger via function argument (cleaner than slog.SetDefault global mutation).
// If the middleware gains a composition-root-injected logger in the future,
// the call site becomes safeObserve(mw.logger, fn) without changing this
// function's contract.
//
// ref: prometheus/client_golang promhttp — instrumentation handler panics
// are silently dropped rather than propagated to the caller.
func safeObserve(logger *slog.Logger, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			if logger == nil {
				logger = slog.Default()
			}
			// Inner recover guards against a panicking logger handler.
			func() {
				defer func() { _ = recover() }()
				logger.Error("observability middleware panic",
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())),
				)
			}()
		}
	}()
	fn()
}
