// Package selector_call_red exercises the most common funnel bypass: a
// direct qualified call types.Implements(...). 1 violation expected
// (declared via spec.Violation()).
package selector_call_red

import (
	"go/types"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func check(t types.Type, i *types.Interface) bool {
	spec.Violation()
	return types.Implements(t, i)
}
