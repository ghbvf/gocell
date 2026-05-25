package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// green_two_loads_nil_and_flat is the compliant defensive idiom: two RunTyped
// calls — once with tags=nil to cover reverse build directives like
// //go:build !integration, then once with archtest.FlatNonDefaultTags() to
// cover all positive tag activations. No for-range loop over
// KnownNonDefaultTags; the rule must NOT catch this.
func _(t *testing.T) {
	scan := func(p *archtest.Pass) []archtest.Diagnostic { return nil }
	_ = archtest.RunTyped(t, archtest.TypedOpts{}, []string{"./..."}, scan)
	_ = archtest.RunTyped(t, archtest.TypedOpts{Tags: archtest.FlatNonDefaultTags()},
		[]string{"./..."}, scan)
}
