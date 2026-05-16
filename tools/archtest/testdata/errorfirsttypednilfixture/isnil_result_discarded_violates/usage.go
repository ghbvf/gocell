// Package isnil_result_discarded_violates is a fixture for
// ERROR-FIRST-TYPED-NIL-01: validation.IsNilInterface result is discarded
// (assigned to _) so the guard is ineffective — 1 violation expected.
package isnil_result_discarded_violates

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New discards the IsNilInterface return value, so the guard is not satisfied.
// Expected violations: 1 (line 14).
func New(dep Dep) (*Service, error) {
	_ = validation.IsNilInterface(dep)
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
