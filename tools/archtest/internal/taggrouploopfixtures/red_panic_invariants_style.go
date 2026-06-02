package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_panic_invariants_style mirrors the historical
// panic_invariants_test.go::TestPanicRegistered pattern: a for-range over
// archtest.KnownNonDefaultTags() with a typed-scope Run (Run(t, Typed(...), ...)) in the
// loop body. Each tagGroup causes a separate packages.Load — the cumulative
// RSS pattern banned by TAGGROUP-LOOP-FORBIDS-RUNTYPED-01.
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Typed(archtest.TypedOpts{Tags: tagGroup},
			[]string{"./..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
