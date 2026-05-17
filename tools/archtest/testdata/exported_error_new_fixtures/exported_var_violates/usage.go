// Package exported_var_violates verifies that a top-level exported
// `var ErrFoo = errors.New(...)` is caught: 1 violation expected
// (declared via spec.Violation()).
package exported_var_violates

import (
	"errors"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

var _ = func() any { spec.Violation(); return nil }()

var ErrFoo = errors.New("foo")
