// Package reason_placeholder_red is a RED fixture for PANIC-REGISTERED-01:
// reason literals must be descriptive, not placeholder identifiers
// (todo / fixme / tbd / xxx / placeholder / wip).
package reason_placeholder_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func TodoReason() {
	panic(panicregister.Approved("todo-fix-later", errcode.Assertion("x"))) // violation
}

func FixmeBare() {
	panic(panicregister.Approved("fixme", errcode.Assertion("x"))) // violation
}

func WipReason() {
	panic(panicregister.Approved("wip-prototype", errcode.Assertion("x"))) // violation
}
