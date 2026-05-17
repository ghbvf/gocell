// Package funclit_multi_stmt_red — FuncLit body has more than 1 statement,
// failing the isAllFalseSentinelFuncLit single-ReturnStmt check;
// 1 violation expected (declared via spec.Violation()).
package funclit_multi_stmt_red

import (
	"go/build/constraint"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func _example(e constraint.Expr) {
	spec.Violation()
	_ = e.Eval(func(_ string) bool {
		_ = 0
		return false
	})
}
