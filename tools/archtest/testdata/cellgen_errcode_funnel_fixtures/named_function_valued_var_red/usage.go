// Package named_function_valued_var_red verifies F9: a named function
// type can hold a reference to a blacklisted constructor. The pre-Ident-
// scan rule (CallExpr.Fun + type assertion to *types.Signature) missed
// this because `o.Type().(*types.Signature)` failed for *types.Named.
// Ident-scan catches the violation at the assignment site — the Ident
// `New` in the RHS resolves via types.Info.Uses[] to errors.New
// regardless of the LHS named type. 1 violation expected at the
// declaration line.
package named_function_valued_var_red

import "errors"

// ErrorCtor is a named function type — historical escape route closed
// by the Ident-scan refactor (PR #574 round-2 Finding 1).
type ErrorCtor func(string) error

var ErrNew ErrorCtor = errors.New

// use exercises the indirect call path; the call site itself is fine
// (ErrNew resolves to *types.Var, not in blacklist). The blacklist hit
// already fired at the declaration above.
func use() error {
	return ErrNew("via named function-typed var")
}
