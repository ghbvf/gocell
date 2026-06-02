//go:build archtest_fixture

// Package autowirefunnelfixture is the planted-bypass fixture for
// BOOTSTRAP-AUTOWIRE-COLLECTOR-FUNNEL-01. It mirrors the shape of the real
// runtime/bootstrap auto-wire (a generic autoWireCachedCollector helper plus
// call sites) and declares both ALLOWED forms and BYPASS forms so the shared
// detector's FIRE path is proven, not just its PASS path on real bootstrap.
//
// Expected detector verdict (asserted by the reverse test):
//   - allowedDirect        — PASS (constructor as the construct argument)
//   - allowedClosure       — PASS (single-return passthrough construct FuncLit)
//   - bypassOtherArg       — FIRE (constructor in the conflictMsg argument)
//   - bypassNaked          — FIRE (naked constructor call, no helper)
//   - bypassSwallowClosure — FIRE (multi-statement degrade-swallowing FuncLit)
//
// Nothing here is wired into production; it exists only to be type-checked and
// scanned by Run(t, Fixture(...), rule).
package autowirefunnelfixture

import (
	"log/slog"

	"github.com/ghbvf/gocell/tools/archtest/internal/autowirefunnelfixture/collectors"
)

type bootstrap struct{ provider collectors.Provider }

// autoWireCachedCollector mirrors runtime/bootstrap.autoWireCachedCollector:
// the construct function is the 3rd positional argument.
func autoWireCachedCollector[T comparable](
	b *bootstrap,
	cache *T,
	construct func(collectors.Provider) (T, error),
	conflictMsg string,
) (T, bool, error) {
	var zero T
	_ = conflictMsg
	if *cache == zero {
		c, err := construct(b.provider)
		if err != nil {
			return zero, false, err
		}
		*cache = c
	}
	return *cache, true, nil
}

// allowedDirect passes the constructor AS the construct argument (direct function
// value) — the event/outbox/projection form. PASS.
func (b *bootstrap) allowedDirect(cache *collectors.Collector) error {
	_, _, err := autoWireCachedCollector(b, cache, collectors.NewAlpha, "conflict msg")
	return err
}

// allowedClosure wraps the constructor in a single-return passthrough FuncLit —
// the HTTP form (which needs an extra config arg, here represented by the inline
// call). PASS.
func (b *bootstrap) allowedClosure(cache *collectors.Collector) error {
	_, _, err := autoWireCachedCollector(b, cache,
		func(p collectors.Provider) (collectors.Collector, error) {
			return collectors.NewBeta(p)
		}, "conflict msg")
	return err
}

// bypassOtherArg references a constructor in the conflictMsg argument (NOT the
// construct slot). The construct slot uses a different constructor, so the
// reference is not routed through the helper's fail-fast. FIRE.
func (b *bootstrap) bypassOtherArg(cache *collectors.Collector) error {
	_, _, err := autoWireCachedCollector(b, cache, collectors.NewAlpha,
		msgFrom(collectors.NewBeta))
	return err
}

func msgFrom(func(collectors.Provider) (collectors.Collector, error)) string { return "conflict msg" }

// bypassNaked calls the constructor with no autoWireCachedCollector at all,
// re-implementing wiring inline. FIRE.
func (b *bootstrap) bypassNaked(p collectors.Provider) (collectors.Collector, error) {
	return collectors.NewAlpha(p)
}

// bypassSwallowClosure routes the constructor through the helper but the construct
// FuncLit is multi-statement: it registers, swallows the conflict error after a
// warn, and returns a nil error — the #1399 degrade re-introduced through the
// funnel. The passthrough lock must FIRE.
func (b *bootstrap) bypassSwallowClosure(cache *collectors.Collector) error {
	_, _, err := autoWireCachedCollector(b, cache,
		func(p collectors.Provider) (collectors.Collector, error) {
			c, cerr := collectors.NewAlpha(p)
			if cerr != nil {
				slog.Warn("degrade: dropping collector family", "err", cerr)
				return c, nil // swallow → helper caches a degraded family
			}
			return c, nil
		}, "conflict msg")
	return err
}
