// Package aliased_validation_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// the validation package is aliased to "val" instead of "validation", so the
// guard is not recognized by the AST-based detector (known gap) — 1 violation expected
// (declared via spec.Violation()).
package aliased_validation_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

var val = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses val.IsNilInterface (aliased) which is not recognized as a guard.
func New(dep Dep) (*Service, error) {
	spec.Violation()
	if val.IsNilInterface(dep) {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
