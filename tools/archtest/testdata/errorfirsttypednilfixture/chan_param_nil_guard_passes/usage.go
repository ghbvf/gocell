// Package chan_param_nil_guard_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// a chan dependency with == nil guard is accepted — 0 violations expected.
package chan_param_nil_guard_passes

// New guards a chan dependency with == nil.
// Expected violations: 0.
func New(events chan int) (*Service, error) {
	if events == nil {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
