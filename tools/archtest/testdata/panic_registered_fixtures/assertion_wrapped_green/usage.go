// Package assertion_wrapped_green verifies that
// panic(panicregister.Approved("test-reason", errcode.Assertion("x")))
// is accepted: 0 violations expected.
package assertion_wrapped_green

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func foo() {
	panic(panicregister.Approved("test-reason", errcode.Assertion("x")))
}
