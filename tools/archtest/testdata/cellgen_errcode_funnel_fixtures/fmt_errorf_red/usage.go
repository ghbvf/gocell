// Package fmt_errorf_red verifies F1: fmt.Errorf (normal SelectorExpr,
// canonical import) is caught by STEP 2 — (fmt, Errorf) is in the
// cellgenErrConstructorBlacklist.
// 1 violation expected.
package fmt_errorf_red

import "fmt"

func foo() error {
	return fmt.Errorf("plain fmt.Errorf")
}
