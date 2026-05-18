// Package errors_new_red verifies F4: errors.New is another error
// constructor route blocked by STEP 3 (callee.Pkg = "errors" ≠ errcode).
// 1 violation expected.
package errors_new_red

import "errors"

func foo() error {
	return errors.New("plain errors.New")
}
