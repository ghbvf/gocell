// Package isnil_inside_non_if_call_violates is a fixture for
// ERROR-FIRST-TYPED-NIL-01: validation.IsNilInterface is called but passed
// to a non-if sink function, so the guard is not an if-condition — 1 violation expected.
package isnil_inside_non_if_call_violates

var validation = struct{ IsNilInterface func(any) bool }{}

func sink(bool) {}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New calls IsNilInterface but passes the result to sink(), not an if condition.
// Expected violations: 1 (line 16).
func New(dep Dep) (*Service, error) {
	sink(validation.IsNilInterface(dep))
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
