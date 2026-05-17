// Package and_compound_violates is a fixture for ERROR-FIRST-TYPED-NIL-01:
// a && compound guard does not fail-fast on the nil dep because the additional
// condition may short-circuit — 1 violation expected (declared via spec.Violation()).
package and_compound_violates

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses && which lets nil flow past when the second cond is false.
func New(dep Dep, strict bool) (*Service, error) {
	spec.Violation()
	if validation.IsNilInterface(dep) && strict {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
