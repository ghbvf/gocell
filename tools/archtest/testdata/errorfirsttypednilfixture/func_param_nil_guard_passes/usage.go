// Package func_param_nil_guard_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// a func dependency with == nil guard is accepted — 0 violations expected.
package func_param_nil_guard_passes

// New guards a func dependency with == nil.
// Expected violations: 0.
func New(handler func() error) (*Service, error) {
	if handler == nil {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
