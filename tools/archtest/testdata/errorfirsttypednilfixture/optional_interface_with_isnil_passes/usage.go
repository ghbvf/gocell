// Package optional_interface_with_isnil_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a New* constructor with an optional interface
// parameter defaulted via validation.IsNilInterface — 0 violations expected.
package optional_interface_with_isnil_passes

var validation = struct{ IsNilInterface func(any) bool }{}

// Reader is an optional dependency interface.
type Reader interface{ Read([]byte) (int, error) }

type defaultReader struct{}

func (defaultReader) Read([]byte) (int, error) { return 0, nil }

// New defaults the optional Reader dep when nil via IsNilInterface.
// Expected violations: 0.
func New(reader Reader) (*Service, error) {
	if validation.IsNilInterface(reader) {
		reader = defaultReader{}
	}
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
