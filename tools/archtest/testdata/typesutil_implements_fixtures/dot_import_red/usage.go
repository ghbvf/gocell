// Package dot_import_red exercises the dot-import bypass: import . "go/types"
// then a bare Implements(...) call. info.Uses still resolves the bare ident
// to the go/types.Implements *types.Func. 1 violation expected
// (declared via spec.Violation()).
package dot_import_red

import (
	. "go/types"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func check(t Type, i *Interface) bool {
	spec.Violation()
	return Implements(t, i)
}
