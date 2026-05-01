// Package func_local_passes verifies that a function-body
// `errors.New(...)` is allowed (the rule targets package-scope exports
// only): 0 violations expected.
package func_local_passes

import "errors"

func Foo() error {
	return errors.New("local err")
}
