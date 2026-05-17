// Package aliased_import_red exercises the import-alias bypass:
// import gt "go/types" then gt.Implements(...). The alias does not change
// the resolved *types.Func object, so info.Uses still points at
// go/types.Implements. 1 violation expected (declared via spec.Violation()).
package aliased_import_red

import (
	gt "go/types"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func check(t gt.Type, i *gt.Interface) bool {
	spec.Violation()
	return gt.Implements(t, i)
}
