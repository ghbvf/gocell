// Package observability holds tiny shared helpers used across kernel/, runtime/
// and adapters/ for emitting structured signals safely.
//
// The package is deliberately minimal — no metric / log / trace abstractions
// live here. Those belong in kernel/observability/metrics, slog, and adapter
// packages respectively. This package exists only to host primitives that
// every layer needs but cannot reach because of GoCell's layer dependency
// rules (e.g. kernel/ cannot import runtime/).
package observability

import (
	"log/slog"
	"runtime/debug"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// SafeObserve runs fn and recovers from any panic, logging it via the provided
// logger instead of letting it escape the calling goroutine.
//
// Use it around observability hooks (metric collectors, log emitters, span
// recorders) whose implementations are supplied by the composition root and
// might panic on faulty input. Panics in such hooks must never crash business
// goroutines — observability is best-effort by design.
//
// Double-layer recover: the outer defer catches fn panics; the inner defer
// inside the log call catches panics from the logger itself. This guarantees
// that even a broken slog.Handler (one that panics in Handle) cannot escape
// SafeObserve. A nil logger falls back to slog.Default().
//
// ref: prometheus/client_golang promhttp — instrumentation handler panics are
// silently dropped rather than propagated to the caller.
func SafeObserve(logger *slog.Logger, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			if logger == nil {
				logger = slog.Default()
			}
			// Inner recover guards against a panicking logger handler.
			func() {
				defer func() { _ = recover() }()
				logger.Error("observability hook panic",
					slog.Any("panic", redaction.RedactAny(v)),
					slog.String("stack", string(debug.Stack())),
				)
			}()
		}
	}()
	fn()
}
