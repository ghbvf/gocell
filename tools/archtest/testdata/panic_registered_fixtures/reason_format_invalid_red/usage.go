// Package reason_format_invalid_red is a RED fixture for PANIC-REGISTERED-01:
// the reason literal must match ^[a-z][a-z0-9-]+$ (kebab-case identifier).
// snake_case, mixed case, leading hyphen, single char all fail.
package reason_format_invalid_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func WithUnderscore() {
	panic(panicregister.Approved("reason_with_underscore", errcode.Assertion("x"))) // violation
}

func WithUppercase() {
	panic(panicregister.Approved("Reason-With-Caps", errcode.Assertion("x"))) // violation
}
