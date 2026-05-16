// Package non_error_constructor_passes is a fixture for
// ERROR-FIRST-TYPED-NIL-01: a New* constructor that does not return error
// is outside the typed-nil rule scope — 0 violations expected.
package non_error_constructor_passes

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New does not return error, so is outside the ERROR-FIRST-TYPED-NIL-01 scope.
// Expected violations: 0.
func New(dep Dep) *Service {
	return &Service{}
}

// Service is a placeholder return type.
type Service struct{}
