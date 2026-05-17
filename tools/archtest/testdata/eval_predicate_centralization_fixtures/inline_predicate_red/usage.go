// Package inline_predicate_red — inline hand-rolled `func(tag string) bool
// { return tag == "X" }` predicate; 1 violation expected (declared via spec.Violation()).
package inline_predicate_red

import (
	"go/build/constraint"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func _example(e constraint.Expr) {
	// Hand-rolled predicate hard-codes a single tag; drifts when toolchain
	// adds defaults. Detection must flag.
	spec.Violation()
	_ = e.Eval(func(tag string) bool { return tag == "X" })
}
