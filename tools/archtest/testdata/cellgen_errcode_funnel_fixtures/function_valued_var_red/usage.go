// Package function_valued_var_red verifies F6: function-valued variable
// re-export. `ErrNew = errors.New` is not itself a CallExpr (no detection
// needed at the declaration site). The call `ErrNew("x")` is a CallExpr
// whose callee resolves to *types.Var (not *types.Func) with signature
// returning single error → STEP 3 fail.
// 1 violation expected.
package function_valued_var_red

import "errors"

var ErrNew = errors.New

func foo() error {
	return ErrNew("via function-valued var")
}
