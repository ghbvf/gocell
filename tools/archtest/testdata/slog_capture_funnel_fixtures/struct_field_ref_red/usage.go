//go:build archtest_fixture

// Package struct_field_ref_red is a RED fixture for SLOG-CAPTURE-GLOBAL-FUNNEL-01's
// blind-spot reverse self-test: slog.SetDefault is bound into a STRUCT FIELD (a
// distinct indirect-reference shape from the function-value var). Like the var
// case it is an *ast.SelectorExpr reference, not a CallExpr, so the direct-call
// main rule misses it; the reverse self-test (info.Uses) must flag it.
package struct_field_ref_red

import "log/slog"

type setterHolder struct {
	fn func(*slog.Logger)
}

// holder binds the banned primitive into a struct field — an indirect reference.
var holder = setterHolder{fn: slog.SetDefault}
