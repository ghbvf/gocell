// Package unexported_var_passes verifies that an unexported sentinel
// `var errFoo = errors.New(...)` is allowed: 0 violations expected.
package unexported_var_passes

import "errors"

var errFoo = errors.New("foo")

// Reference to keep go-vet happy and confirm errFoo is used.
func Foo() error { return errFoo }
