package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_standalone_module_scope is the per-member trip-wire for the
// StandaloneModule typed scope: a for-range over archtest.KnownNonDefaultTags()
// with archtest.Run(t, StandaloneModule(...), ...) inside the loop body.
// StandaloneModule does a per-tag packages.Load (rooted at a caller-supplied
// dir) just like Typed, so the cumulative-RSS pathology applies. The precision
// gate's expectedRed entry locks "StandaloneModule" membership in
// taggroupTypedScopeCtors.
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.StandaloneModule("/tmp/fixturemod",
			archtest.TypedOpts{Tags: tagGroup}, []string{"./..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
