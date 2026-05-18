// Package fmt_dot_import_errorf_red verifies F3: dot import of fmt
// renders the call as a bare Ident (Errorf), not a SelectorExpr.
// resolveCellgenCallee's *ast.Ident branch resolves types.Info.Uses
// to the same fmt.Errorf object → STEP 3 catches.
// 1 violation expected.
package fmt_dot_import_errorf_red

import . "fmt"

func foo() error {
	return Errorf("dot-imported fmt.Errorf")
}
