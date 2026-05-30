//go:build archtest_fixture

// Package redunguardedparam is a RED fixture for SAGA-CONSTRUCTOR-NIL-GUARD-01.
// It declares a New* constructor that accepts a non-variadic interface-typed
// parameter without calling IsNilInterface or clock.MustHaveClock on it.
// A guarded counterpart (NewGuarded) is the negative control proving the detector
// does not fire for properly guarded constructors.
// Loaded only via RunTypedFixture.
package redunguardedparam

import "github.com/ghbvf/gocell/pkg/validation"

// MyInterface is a minimal interface for fixture purposes.
type MyInterface interface {
	Do() error
}

// NewUnguarded accepts a MyInterface parameter but never calls IsNilInterface
// or MustHaveClock on it. The detector must fire for this constructor.
func NewUnguarded(dep MyInterface) *struct{} {
	_ = dep
	return &struct{}{}
}

// NewGuarded is the negative control: it calls IsNilInterface before using dep.
// The detector must NOT fire for this constructor.
func NewGuarded(dep MyInterface) (*struct{}, error) {
	if validation.IsNilInterface(dep) {
		return nil, nil
	}
	_ = dep
	return &struct{}{}, nil
}
