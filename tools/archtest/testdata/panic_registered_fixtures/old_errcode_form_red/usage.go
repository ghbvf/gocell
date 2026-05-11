// Package old_errcode_form_red verifies that panic(errcode.Assertion("x"))
// without Approved wrap is caught: 1 violation expected.
package old_errcode_form_red

import "github.com/ghbvf/gocell/pkg/errcode"

func foo() {
	panic(errcode.Assertion("x"))
}
