// Package approved_wrapper_green is the GREEN baseline: it routes through
// the sanctioned typesutil.ImplementsInterface funnel and never references
// go/types.Implements directly. 0 violations expected.
package approved_wrapper_green

import (
	"go/types"

	"github.com/ghbvf/gocell/tools/typesutil"
)

func check(t types.Type, i *types.Interface) bool {
	return typesutil.ImplementsInterface(t, i)
}
