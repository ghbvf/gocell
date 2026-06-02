package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_fixture_scope is the per-member trip-wire for the Fixture typed scope: a
// for-range over archtest.KnownNonDefaultTags() with
// archtest.Run(t, Fixture(...), ...) inside the loop body. Fixture carries no
// Tags (the tag group is unused — `for range` binds nothing), but it still does
// a per-iteration packages.Load, so the cumulative-RSS pathology applies and the
// detector includes Fixture defensively in taggroupTypedScopeCtors. This fixture
// locks that membership.
func _(t *testing.T) {
	for range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{}, []string{"./..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
