// Package dot_import_red exercises the dot-import bypass: import . "go/types"
// then a bare Implements(...) call. info.Uses still resolves the bare ident
// to the go/types.Implements *types.Func. 1 violation expected.
package dot_import_red

import . "go/types"

func check(t Type, i *Interface) bool {
	return Implements(t, i)
}
