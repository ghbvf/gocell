// Package reason_const_ident_red is a RED fixture for PANIC-REGISTERED-01:
// the reason argument must be a *ast.BasicLit STRING literal, not a const
// identifier reference (which is also a const string at types level but
// fails the literal-shape check).
//
// 1 violation expected (declared via spec.Violation()).
package reason_const_ident_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

const reasonConst = "test-const-reason"

func Foo() {
	spec.Violation()
	panic(panicregister.Approved(reasonConst, errcode.Assertion("x")))
}
