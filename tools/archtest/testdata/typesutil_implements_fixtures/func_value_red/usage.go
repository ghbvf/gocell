// Package func_value_red exercises the func-value bypass: types.Implements
// is taken as a value (f := types.Implements) and never syntactically
// "called" at this site. A CallExpr-only walk would miss this; the
// info.Uses sweep catches it because the Implements ident still resolves to
// the go/types.Implements *types.Func. 1 violation expected — this fixture
// is the evidence that the sweep structurally subsumes the CallExpr-walk
// blind spot (declared via spec.Violation()).
package func_value_red

import (
	"go/types"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func register() any {
	spec.Violation()
	f := types.Implements
	return f
}
