// Package non_constructor_function_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a function returning error but not named New*
// is outside the typed-nil constructor rule scope — 0 violations expected.
package non_constructor_function_passes

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// Build is not named New* so is outside ERROR-FIRST-TYPED-NIL-01 scope.
// Expected violations: 0.
func Build(dep Dep) (*Service, error) {
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
