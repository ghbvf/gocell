// Package then_in_defer_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// the nil-handling return is inside a deferred FuncLit, not the constructor
// body — the guard is not satisfied — 1 violation expected.
package then_in_defer_violates

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New has the guard inside a defer, which does not satisfy fail-fast.
// Expected violations: 1 (line 13).
func New(dep Dep) (*Service, error) {
	if validation.IsNilInterface(dep) {
		defer func() { _ = 1 }()
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
