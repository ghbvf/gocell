package bootstrap

// metrics_autowire.go — single source of the bootstrap metric-collector
// auto-wire discipline.
//
// runtime/bootstrap auto-wires several metric collector families at startup
// (HTTP requests, event-router subscriptions, outbox rejects, projection
// metrics). They all share the same control flow: skip when no real provider is
// configured, construct the collector exactly once and cache it, and treat a
// registration conflict as a startup-fatal error — never a slog.Warn-then-degrade
// that silently drops the metric family.
//
// That discipline used to be hand-copied into each auto-wire function. #1399
// (the projection PR-04a regression) proved the copy is a drift surface: an AI
// co-author transcribed a wrong description of the HTTP collector into the
// projection path, warn-degrading instead of failing fast, so the second
// projection silently lost its metrics. autoWireCachedCollector is the single
// source that makes the four call sites byte-identical in their skip/cache/
// fail-fast behavior; the BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01 archtest keeps
// every collector construction routed through it.

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// autoWireCachedCollector registers a metrics collector exactly ONCE, caching it
// via *cache so repeated wiring (one per listener / cell / projection) reuses the
// same instrument instead of re-registering a fixed-name metric family — the
// duplicate registration the second caller would otherwise hit.
//
// Behavior:
//   - nil or NopProvider → skip: returns (zero, false, nil); construct is not run.
//   - cache already populated → returns the cached value with wired=true.
//   - otherwise constructs once, caches, returns (value, true, nil).
//   - construct error → startup-fatal: returns it wrapped with conflictMsg.
//     This is the load-bearing rule — there is NO warn-then-degrade path, so a
//     registration conflict can never silently disable a metric family.
//
// The cached collector is also returned as val with wired=true so callers that
// need the value immediately (HTTP option-wrap, outbox AttachObserver) can use
// it; callers that only need the side effect of populating *cache (projection)
// may discard val/wired and just check err.
//
// Preconditions:
//   - cache must be non-nil (pass &b.someField); a nil cache is a programmer
//     error and panics at the *cache deref.
//   - T is constrained to comparable so *cache can be tested against the zero
//     value. Both pointer types and pointer-backed interfaces (the four current
//     callers) satisfy it; comparing against the nil zero value never panics
//     (a nil-interface zero differs in dynamic type from any populated value, so
//     the comparison short-circuits before any value compare).
//   - construct must return a non-nil value on success. A typed-nil interface
//     (non-nil interface wrapping a nil pointer) would compare unequal to the
//     zero value and be cached as if populated; none of the four collector
//     constructors do this.
//
// conflictMsg is the actionable, per-collector message (everything before the
// wrapped cause); the helper appends ": <cause>".
func autoWireCachedCollector[T comparable](
	b *Bootstrap,
	cache *T,
	construct func(kernelmetrics.Provider) (T, error),
	conflictMsg string,
) (val T, wired bool, err error) {
	var zero T
	if b.metricsProvider == nil {
		return zero, false, nil
	}
	// NopProvider is the default when no provider is injected; skip auto-wire to
	// avoid allocating a no-op collector on every bootstrap startup.
	if _, isNop := b.metricsProvider.(kernelmetrics.NopProvider); isNop {
		return zero, false, nil
	}
	if *cache == zero {
		c, cerr := construct(b.metricsProvider)
		if cerr != nil {
			return zero, false, fmt.Errorf("%s: %w", conflictMsg, cerr)
		}
		*cache = c
	}
	return *cache, true, nil
}
