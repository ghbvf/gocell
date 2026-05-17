// Package payload_type_invalid_red is a RED fixture for PANIC-REGISTERED-01:
// the payload argument must be *errcode.Error or interface{}; bare error
// from fmt.Errorf (typed as error interface with Error() method) is rejected.
//
// 3 violations expected (declared via spec.Violation()).
package payload_type_invalid_red

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/panicregister"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func WithFmtErrorf() {
	err := fmt.Errorf("bad thing")
	spec.Violation()
	panic(panicregister.Approved("payload-fmt-errorf-red", err))
}

func WithBareString() {
	msg := "not-allowed-string"
	spec.Violation()
	panic(panicregister.Approved("payload-string-red", msg))
}

func WithStringLiteral() {
	spec.Violation()
	panic(panicregister.Approved("payload-string-literal-red", "literal-bad"))
}
