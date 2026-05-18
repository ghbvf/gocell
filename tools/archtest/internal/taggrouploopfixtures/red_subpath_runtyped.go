package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_subpath_runtyped uses a subpath pattern (./cells/...) rather than the
// whole module ./... — the cumulative-RSS pathology applies to any patterns
// shape because each tagGroup is a separate cacheKey under the same patterns
// shape. The rule must catch RangeStmt+RunTyped regardless of the patterns
// arg, since patterns is irrelevant to the loop-amortization invariant.
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		_ = archtest.RunTyped(t, archtest.TypedOpts{Tags: tagGroup},
			[]string{"./cells/..."},
			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
