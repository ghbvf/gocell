// Package reason_const_ident_red is a RED fixture for PANIC-REGISTERED-01:
// the reason argument must be a *ast.BasicLit STRING literal, not a const
// identifier reference (which is also a const string at types level but
// fails the literal-shape check).
package reason_const_ident_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

const reasonConst = "test-const-reason"

func Foo() {
	panic(panicregister.Approved(reasonConst, errcode.Assertion("x"))) // violation on this line
}
