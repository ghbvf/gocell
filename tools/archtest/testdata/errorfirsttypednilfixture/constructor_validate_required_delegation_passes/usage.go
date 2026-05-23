// Package constructor_validate_required_delegation_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a New* constructor whose nil-able interface
// parameter is NOT inline-guarded but is delegated to the generated
// validateRequired() funnel (REQUIRED-DEP-NIL-GUARD-01). The funnel is the
// guard, so the scanner must report 0 violations — re-requiring an inline guard
// would contradict A3 (no hand-written IsNilInterface on required fields).
package constructor_validate_required_delegation_passes

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// Service holds the dependency.
type Service struct {
	dep Dep
}

// validateRequired stands in for the generated REQUIRED-DEP-NIL-GUARD funnel
// method (which checks each gocell:"required" field for nil).
func (s *Service) validateRequired() error { return nil }

// New delegates required-dep nil checking to validateRequired() with no inline
// per-param guard. Expected violations: 0.
func New(dep Dep) (*Service, error) {
	s := &Service{dep: dep}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}
