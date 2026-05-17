// Package func_value_red is the RED fixture for the fixturespec.Violation
// form-uniqueness rule. It contains two shapes the funnel must distinguish:
//
//   - line 16: spec.Violation() — direct call, the only approved marker form.
//   - line 17-18: f := spec.Violation; f() — func-value bypass; ResolvePackageRef
//     returns false for `f()` (info.Uses[f] is *types.Var, not *types.Func), and
//     the SelectorExpr `spec.Violation` on line 17 is not inside any CallExpr.
//     Both halves are missed by the existing CallExpr-only walk.
//
// 1 spec.Violation() marker expected; Wave 2's detectFixturespecValuePosition
// must emit ≥1 Diagnostic for the line-17 assignment so the funnel becomes
// form-unique (only direct calls count; assignment form is rejected).
package func_value_red

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func init() {
	spec.Violation()
	f := spec.Violation
	f()
}
