// Package aliased_validation_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// the validation package is aliased to "val" instead of "validation", so the
// guard is not recognized by the AST-based detector (known gap) — 1 violation expected.
package aliased_validation_violates

var val = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses val.IsNilInterface (aliased) which is not recognized as a guard.
// Expected violations: 1 (line 13).
func New(dep Dep) (*Service, error) {
	if val.IsNilInterface(dep) {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
