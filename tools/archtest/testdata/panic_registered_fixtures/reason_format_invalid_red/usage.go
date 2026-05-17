// Package reason_format_invalid_red is a RED fixture for PANIC-REGISTERED-01:
// the reason literal must match ^[a-z][a-z0-9-]+$ (kebab-case identifier).
// snake_case, mixed case, leading hyphen, single char all fail.
//
// 2 violations expected (declared via spec.Violation()).
package reason_format_invalid_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func WithUnderscore() {
	spec.Violation()
	panic(panicregister.Approved("reason_with_underscore", errcode.Assertion("x")))
}

func WithUppercase() {
	spec.Violation()
	panic(panicregister.Approved("Reason-With-Caps", errcode.Assertion("x")))
}
