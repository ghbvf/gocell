// Package form_b_good — Form B (inline `func(_ string) bool { return false }`
// all-false sentinel) is the canonical accepted shape; 0 violations expected.
package form_b_good

import "go/build/constraint"

func _example(e constraint.Expr) {
	_ = e.Eval(func(_ string) bool { return false })
}
