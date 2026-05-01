// Package exported_var_violates verifies that a top-level exported
// `var ErrFoo = errors.New(...)` is caught: 1 violation expected.
package exported_var_violates

import "errors"

var ErrFoo = errors.New("foo")
