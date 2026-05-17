// Package selector_call_red exercises the most common funnel bypass: a
// direct qualified call types.Implements(...). 1 violation expected at the
// line of the Implements selector ident.
package selector_call_red

import "go/types"

func check(t types.Type, i *types.Interface) bool {
	return types.Implements(t, i)
}
