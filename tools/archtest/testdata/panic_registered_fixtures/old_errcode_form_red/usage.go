// Package old_errcode_form_red verifies that panic(errcode.Assertion("x"))
// without Approved wrap is caught: 1 violation expected (declared via spec.Violation()).
package old_errcode_form_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func foo() {
	spec.Violation()
	panic(errcode.Assertion("x"))
}
