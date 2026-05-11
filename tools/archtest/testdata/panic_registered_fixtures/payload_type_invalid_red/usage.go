// Package payload_type_invalid_red is a RED fixture for PANIC-REGISTERED-01:
// the payload argument must be *errcode.Error or interface{}; bare error
// from fmt.Errorf (typed as error interface with Error() method) is rejected.
package payload_type_invalid_red

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/panicregister"
)

func WithFmtErrorf() {
	err := fmt.Errorf("bad thing")
	panic(panicregister.Approved("payload-fmt-errorf-red", err)) // violation
}

func WithBareString() {
	msg := "not-allowed-string"
	panic(panicregister.Approved("payload-string-red", msg)) // violation
}

func WithStringLiteral() {
	panic(panicregister.Approved("payload-string-literal-red", "literal-bad")) // violation
}
