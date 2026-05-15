// Package slice_param_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// slice parameters are outside the nil-able rule scope (nil slice is safe
// to len/range) — 0 violations expected.
package slice_param_passes

// New accepts a slice param which is not in scope for the nil guard rule.
// Expected violations: 0.
func New(items []int) (*Service, error) {
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
