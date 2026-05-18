// Package errors_dot_import_new_red verifies F5: dot import of errors
// renders the call as a bare Ident (New). STEP 1's *ast.Ident branch
// covers, STEP 3 catches (callee.Pkg = "errors" ≠ errcode).
// 1 violation expected.
package errors_dot_import_new_red

import . "errors"

func foo() error {
	return New("dot-imported errors.New")
}
