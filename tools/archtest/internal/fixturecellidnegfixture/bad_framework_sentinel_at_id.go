//go:build archtest_fixture

package fixturecellidnegfixture

import (
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// BadFrameworkSentinelAtID places metadata.FrameworkOwnerSentinel at CellMeta.ID —
// a REAL-cell position, not an owner/provider one. A1 MUST flag it: "_framework" is
// not a legal cell id (it names the framework, never a cell), and A1 is
// field-position-aware (allowFrameworkSentinel is false for CellMeta.ID). This is the
// RED counterpart to GoodFrameworkSentinel{,OtherEndpoints}; without it the
// field-aware gate could regress to "accept sentinel everywhere" and stay green.
var BadFrameworkSentinelAtID = &metadata.CellMeta{
	ID: metadata.FrameworkOwnerSentinel,
}
