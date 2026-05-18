// Package errcode_assertion_green exercises the three current errcode
// whitelist members (Assertion, New, Wrap). 0 violations expected; golden
// file is empty.
//
// errcode.WithDetails / WithInternal return Option (not error) and are
// filtered out by STEP 2; they don't need explicit allowlisting.
package errcode_assertion_green

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func foo() *errcode.Error {
	return errcode.Assertion("assertion message")
}

func bar() *errcode.Error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "validation",
		errcode.WithDetails(slog.String("field", "value")))
}

func baz(cause error) *errcode.Error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "wrap message", cause)
}
