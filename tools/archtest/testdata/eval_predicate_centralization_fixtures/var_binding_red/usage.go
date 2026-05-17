// Package var_binding_red — var binding indirection (`var p = func(...) { ... };
// expr.Eval(p)`) bypasses inline-FuncLit requirement; 1 violation expected
// (declared via spec.Violation()).
package var_binding_red

import (
	"go/build/constraint"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func _example(e constraint.Expr) {
	p := func(_ string) bool { return false }
	spec.Violation()
	_ = e.Eval(p)
}
