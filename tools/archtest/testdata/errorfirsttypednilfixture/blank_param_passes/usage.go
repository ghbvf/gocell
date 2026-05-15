// Package blank_param_passes is a fixture for ERROR-FIRST-TYPED-NIL-01:
// blank-name (_) interface params are intentionally outside the rule scope
// because _ is unaddressable and cannot appear in a guard expression — 0 violations expected.
package blank_param_passes

// Dep is a sample interface dependency.
type Dep interface{ Do() }

// New uses a blank-name param which is outside ERROR-FIRST-TYPED-NIL-01 scope.
// Expected violations: 0.
func New(_ Dep) (*Service, error) {
	return &Service{}, nil
}

// Service is a placeholder return type.
type Service struct{}
