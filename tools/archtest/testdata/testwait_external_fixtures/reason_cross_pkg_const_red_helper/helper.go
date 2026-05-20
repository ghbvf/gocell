//go:build archtest_fixture

// Package reason_cross_pkg_const_red_helper exposes a string const used by
// the reason_cross_pkg_const_red fixture to test that cross-package SelectorExpr
// const references are rejected by TEST-POLLING-EXTERNAL-REASON-LITERAL-01.
package reason_cross_pkg_const_red_helper

// ReasonConst is a valid kebab-case string that would pass format validation,
// but since it is referenced as a SelectorExpr (pkg.Const) rather than a
// *ast.BasicLit, the archtest "not BasicLit" check rejects it.
const ReasonConst = "kebab-case-reason"
