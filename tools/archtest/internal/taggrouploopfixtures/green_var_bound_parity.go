package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// green_var_bound_parity is the legitimate var-binding idiom the rule must
// NOT catch: KnownNonDefaultTags() is bound to a var and inspected (here a
// length assertion) WITHOUT a tagGroup for-range driving Run(t, Typed(...)). This
// mirrors the real production pass_test.go façade↔oracle parity assertion.
// BS-4's closure must stay precise: bind-without-loop+Run(t, Typed(...)) is not a
// cumulative-RSS pathology and must produce no diagnostic.
func _(t *testing.T) {
	facadeKnown := archtest.KnownNonDefaultTags()
	if len(facadeKnown) == 0 {
		t.Fatal("KnownNonDefaultTags returned empty")
	}
}
