// Package if_cond_no_return_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// the if cond matches IsNilInterface but the then-branch does not return or
// assign the param, so the guard is not satisfied — 1 violation expected
// (declared via spec.Violation()).
package if_cond_no_return_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New has the IsNilInterface call in the if condition but then-branch is a no-op.
func New(dep Dep) (*Service, error) {
	spec.Violation()
	if validation.IsNilInterface(dep) {
		_ = 1
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
