// Package must_prefix_wrapped_green verifies that a Must*-prefixed function
// using the Approved funnel is accepted: 0 violations expected.
package must_prefix_wrapped_green

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func MustFoo() {
	panic(panicregister.Approved("must-foo-init", errcode.Assertion("x")))
}
