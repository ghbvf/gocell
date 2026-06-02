package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_production_scope is the per-member trip-wire for the Production typed
// scope: a for-range over archtest.KnownNonDefaultTags() with
// archtest.Run(t, Production(...), ...) inside the loop body. Production does a
// per-tag packages.Load just like Typed, so the cumulative-RSS pathology
// TAGGROUP-LOOP-FORBIDS-RUNTYPED-01 forbids applies. If "Production" were dropped
// from taggroupTypedScopeCtors this fixture would stop tripping — the precision
// gate's expectedRed entry locks that membership.
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Production(archtest.TypedOpts{Tags: tagGroup}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
