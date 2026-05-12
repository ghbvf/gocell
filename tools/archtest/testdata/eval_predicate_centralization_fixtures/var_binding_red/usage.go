// Package var_binding_red — var binding indirection (`var p = func(...) { ... };
// expr.Eval(p)`) bypasses inline-FuncLit requirement; 1 violation expected at
// the call site (Eval arg is *ast.Ident, not CallExpr or FuncLit).
package var_binding_red

import "go/build/constraint"

func _example(e constraint.Expr) {
	p := func(_ string) bool { return false }
	_ = e.Eval(p)
}
