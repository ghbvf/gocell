// Package funclit_multi_stmt_red — FuncLit body has more than 1 statement,
// failing the isAllFalseSentinelFuncLit single-ReturnStmt check;
// 1 violation expected at line 9.
package funclit_multi_stmt_red

import "go/build/constraint"

func _example(e constraint.Expr) {
	_ = e.Eval(func(_ string) bool {
		_ = 0
		return false
	})
}
