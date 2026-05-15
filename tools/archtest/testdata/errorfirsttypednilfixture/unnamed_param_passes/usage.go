// Package unnamed_param_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// unnamed (type-only) interface params are intentionally outside the rule scope
// because they cannot be referred to in a guard expression — 0 violations expected.
package unnamed_param_passes

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses a type-only (unnamed) param which is outside ERROR-FIRST-TYPED-NIL-01 scope.
// Expected violations: 0.
func New(Dep) (*Service, error) {
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
