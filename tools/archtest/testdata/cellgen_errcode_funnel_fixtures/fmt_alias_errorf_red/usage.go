// Package fmt_alias_errorf_red verifies F2: aliased fmt import
// (fmtx "fmt") + fmtx.Errorf is caught by STEP 3 — types.Info resolves
// the callee to fmt.Errorf regardless of import alias.
// 1 violation expected.
package fmt_alias_errorf_red

import fmtx "fmt"

func foo() error {
	return fmtx.Errorf("aliased fmt.Errorf")
}
