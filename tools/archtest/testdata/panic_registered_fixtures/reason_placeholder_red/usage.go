// Package reason_placeholder_red is a RED fixture for PANIC-REGISTERED-01:
// reason literals must be descriptive, not placeholder identifiers
// (todo / fixme / tbd / xxx / placeholder / wip).
//
// 3 violations expected (declared via spec.Violation()).
package reason_placeholder_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func TodoReason() {
	spec.Violation()
	panic(panicregister.Approved("todo-fix-later", errcode.Assertion("x")))
}

func FixmeBare() {
	spec.Violation()
	panic(panicregister.Approved("fixme", errcode.Assertion("x")))
}

func WipReason() {
	spec.Violation()
	panic(panicregister.Approved("wip-prototype", errcode.Assertion("x")))
}
