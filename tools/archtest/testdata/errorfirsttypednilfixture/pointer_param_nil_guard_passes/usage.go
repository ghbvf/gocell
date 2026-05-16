// Package pointer_param_nil_guard_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a pointer dependency with == nil guard is accepted
// (pointers do not have typed-nil) — 0 violations expected.
package pointer_param_nil_guard_passes

// Pool is a concrete pointer dep.
type Pool struct{}

// New guards a pointer dependency with == nil, which is sufficient.
// Expected violations: 0.
func New(pool *Pool) (*Service, error) {
	if pool == nil {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
