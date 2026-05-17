// Package constructor_interface_without_isnil_violates is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a New* constructor with an interface parameter
// guarded only by == nil (not validation.IsNilInterface) — 1 violation expected
// (declared via spec.Violation()).
package constructor_interface_without_isnil_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses == nil to check an interface parameter, which cannot defeat typed-nil.
func New(dep Dep) (*Service, error) {
	spec.Violation()
	if dep == nil {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
