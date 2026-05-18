// Package errcode_assertion_green exercises the four current errcode
// constructor functions returned by *types.Func with Pkg.Path() ==
// pkg/errcode (Assertion / New / Wrap / WrapInfra). All four are NOT in
// the stdlib constructor blacklist, so STEP 2 doesn't fail. 0 violations
// expected; golden file is empty.
//
// errcode.WithDetails / WithInternal return Option (not error) and are
// filtered out by the result-type check inside STEP 3 (only used for
// *types.Var path); the Func path doesn't apply the error filter, but
// these Options aren't in the blacklist either, so both routes leave
// them alone.
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

func qux(cause error) *errcode.Error {
	return errcode.WrapInfra(errcode.ErrInternal, "infra wrap", cause)
}
