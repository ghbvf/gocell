// Package fmt_errorf_red verifies F1: fmt.Errorf (normal SelectorExpr,
// canonical import) is caught by STEP 3 (callee.Pkg = "fmt" ≠ errcode).
// 1 violation expected.
package fmt_errorf_red

import "fmt"

func foo() error {
	return fmt.Errorf("plain fmt.Errorf")
}
