// Package or_compound_isnil_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// a || compound guard where one leaf is IsNilInterface is accepted because
// either condition alone ensures fail-fast — 0 violations expected.
package or_compound_isnil_passes

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses || which still fail-fasts on nil dep via the IsNilInterface leaf.
// Expected violations: 0.
func New(dep Dep, strict bool) (*Service, error) {
	if validation.IsNilInterface(dep) || strict {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
