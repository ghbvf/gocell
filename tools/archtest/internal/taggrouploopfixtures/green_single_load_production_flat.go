package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// green_single_load_production_flat is the compliant idiom: a single
// archtest.RunTyped call with Tags = archtest.FlatNonDefaultTags() — one
// packages.Load covers all tag-gated production file sets via go/build
// matchTag's "tag ∈ BuildTags" semantics. The rule must NOT catch this.
func _(t *testing.T) {
	_ = archtest.RunTyped(t, archtest.TypedOpts{Tags: archtest.FlatNonDefaultTags()},
		[]string{"./..."},
		func(p *archtest.Pass) []archtest.Diagnostic { return nil })
}
