//go:build archtest_fixture

// Package redunguardedparam is a RED fixture for SAGA-CONSTRUCTOR-NIL-GUARD-01.
// It declares a New* constructor that accepts a non-variadic interface-typed
// parameter without calling IsNilInterface or clock.MustHaveClock on it.
// A guarded counterpart (NewGuarded) is the negative control proving the detector
// does not fire for properly guarded constructors.
// Loaded only via Run(t, Fixture(...)).
package redunguardedparam

import (
	"errors"

	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// errDepRequired models the fail-fast error a real constructor returns when a
// required dependency is nil.
var errDepRequired = errors.New("redunguardedparam: dep required")

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
// The detector must NOT fire for this constructor. A real constructor returns a
// fail-fast errcode error in the guard branch; this fixture returns a sentinel
// error to model that shape (only the guard *call* matters to the detector).
func NewGuarded(dep MyInterface) (*struct{}, error) {
	if validation.IsNilInterface(dep) {
		return nil, errDepRequired
	}
	return &struct{}{}, nil
}

// NewParenGuarded guards dep through a parenthesized argument
// (IsNilInterface((dep))). The detector must still recognize this as a guard
// (ast.Unparen) and NOT fire — the regression guard for the parenthesized-arg
// bypass (F2/B3).
func NewParenGuarded(dep MyInterface) (*struct{}, error) {
	if validation.IsNilInterface((dep)) {
		return nil, errDepRequired
	}
	return &struct{}{}, nil
}
