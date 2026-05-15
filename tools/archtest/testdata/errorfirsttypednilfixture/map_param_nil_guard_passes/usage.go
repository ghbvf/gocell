// Package map_param_nil_guard_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// a map dependency with == nil guard is accepted — 0 violations expected.
package map_param_nil_guard_passes

// New guards a map dependency with == nil.
// Expected violations: 0.
func New(routes map[string]int) (*Service, error) {
	if routes == nil {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
