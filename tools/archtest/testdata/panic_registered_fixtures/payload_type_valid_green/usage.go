// Package payload_type_valid_green is a GREEN fixture for PANIC-REGISTERED-01:
// payload is *errcode.Error (from errcode.Assertion or from an upstream
// constructor returning *errcode.Error) or interface{} (recovered value).
package payload_type_valid_green

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

func WithAssertionCall() {
	panic(panicregister.Approved("payload-assertion-green", errcode.Assertion("ok")))
}

func WithStructuredErrPointer() {
	var ec *errcode.Error = errcode.Assertion("via-variable")
	panic(panicregister.Approved("payload-struct-err-green", ec))
}

func WithRecover(r any) {
	// Simulates a C-class re-throw: r is the recovered value (interface{}/any).
	// The bare inner panic is intentionally absent to keep this a GREEN fixture;
	// see recovered_value_green for the full defer/recover pattern.
	if r != nil {
		panic(panicregister.Approved("payload-rethrow-green", r))
	}
}
