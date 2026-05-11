// Package form_a_good — Form A (typeseval.BuildContextPredicate CallExpr) is
// the canonical accepted shape; 0 violations expected.
package form_a_good

import (
	"go/build/constraint"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

func _example(e constraint.Expr) {
	_ = e.Eval(typeseval.BuildContextPredicate())
	_ = e.Eval(typeseval.BuildContextPredicate("integration"))
}
