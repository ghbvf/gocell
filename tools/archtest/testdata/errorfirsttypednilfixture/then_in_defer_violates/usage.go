// Package then_in_defer_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// the nil-handling return is inside a deferred FuncLit, not the constructor
// body — the guard is not satisfied — 1 violation expected (declared via spec.Violation()).
package then_in_defer_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New has the guard inside a defer, which does not satisfy fail-fast.
func New(dep Dep) (*Service, error) {
	spec.Violation()
	if validation.IsNilInterface(dep) {
		defer func() { _ = 1 }()
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
