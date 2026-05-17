// Package isnil_result_discarded_violates is a fixture for
// ERROR-FIRST-TYPED-NIL-01: validation.IsNilInterface result is discarded
// (assigned to _) so the guard is ineffective — 1 violation expected
// (declared via spec.Violation()).
package isnil_result_discarded_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New discards the IsNilInterface return value, so the guard is not satisfied.
func New(dep Dep) (*Service, error) {
	spec.Violation()
	_ = validation.IsNilInterface(dep)
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
