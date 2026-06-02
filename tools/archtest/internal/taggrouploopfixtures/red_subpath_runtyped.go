package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_subpath_runtyped uses a subpath pattern (./cells/...) rather than the
// whole module ./... — the cumulative-RSS pathology applies to any patterns
// shape because each tagGroup is a separate cacheKey under the same patterns
// shape. The rule must catch a RangeStmt containing a typed-scope Run
// (Run(t, Typed(...), ...)) regardless of the patterns arg, since patterns
// is irrelevant to the loop-amortization invariant.
//
// Note: the "subpath" in the filename refers to the patterns arg (./cells/...)
// passed to the Run(t, Typed(...)) call, not the call's position in code.
// The point of this fixture is patterns-arg variance: the tagGroup-loop-
// amortization invariant is independent of the patterns shape (full ./... vs subpath alike).
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Typed(archtest.TypedOpts{Tags: tagGroup},
			[]string{"./cells/..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
