// Package multiple_specs_violates verifies that all exported
// `Err* = errors.New(...)` specs inside a single var block are caught:
// 2 violations expected (declared via spec.Violation()).
package multiple_specs_violates

import (
	"errors"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

var (
	_ = func() any { spec.Violation(); return nil }()
	_ = func() any { spec.Violation(); return nil }()

	ErrAlpha = errors.New("alpha")
	ErrBeta  = errors.New("beta")
)
