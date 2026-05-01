// Package multiple_specs_violates verifies that all exported
// `Err* = errors.New(...)` specs inside a single var block are caught:
// 2 violations expected (lines 9 and 10).
package multiple_specs_violates

import "errors"

var (
	ErrAlpha = errors.New("alpha")
	ErrBeta  = errors.New("beta")
)
