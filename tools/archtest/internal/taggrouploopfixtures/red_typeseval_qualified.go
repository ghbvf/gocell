package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// red_typeseval_qualified ranges over typeseval.KnownNonDefaultTags() rather
// than archtest.KnownNonDefaultTags(). The rule must resolve both façade
// re-export and direct typeseval reference through *types.Info: same
// underlying function object, two import shapes.
func _(t *testing.T) {
	for _, tagGroup := range typeseval.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Typed(archtest.TypedOpts{Tags: tagGroup},
			[]string{"./tools/codegen/cellgen/..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
