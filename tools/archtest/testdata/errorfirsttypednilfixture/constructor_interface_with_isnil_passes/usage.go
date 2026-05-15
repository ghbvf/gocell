// Package constructor_interface_with_isnil_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a New* constructor with an interface parameter
// correctly guarded by validation.IsNilInterface — 0 violations expected.
package constructor_interface_with_isnil_passes

var validation = struct{ IsNilInterface func(any) bool }{}

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses validation.IsNilInterface which defeats typed-nil.
// Expected violations: 0.
func New(dep Dep) (*Service, error) {
	if validation.IsNilInterface(dep) {
		return nil, nil
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
