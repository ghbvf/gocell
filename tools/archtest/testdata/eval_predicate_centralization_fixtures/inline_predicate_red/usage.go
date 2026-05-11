// Package inline_predicate_red — inline hand-rolled `func(tag string) bool
// { return tag == "X" }` predicate; 1 violation expected at line 9.
package inline_predicate_red

import "go/build/constraint"

func _example(e constraint.Expr) {
	// Hand-rolled predicate hard-codes a single tag; drifts when toolchain
	// adds defaults. Detection must flag.
	_ = e.Eval(func(tag string) bool { return tag == "X" })
}
