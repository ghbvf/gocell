// Package errors_join_red verifies F8: errors.Join is a stdlib error
// combiner that returns error from input errors. It's not "construction
// from scratch" but creates a new error value joining existing ones —
// listed in cellgenErrConstructorBlacklist for completeness (parallel
// to fmt.Errorf and errors.New). STEP 2 fail.
// 1 violation expected.
package errors_join_red

import "errors"

func foo(a, b error) error {
	return errors.Join(a, b)
}
