//go:build archtest_fixture

// Package redanonymousparam is a RED fixture for SAGA-CONSTRUCTOR-NIL-GUARD-01:
// interface parameters that cannot be nil-guarded because they have no usable
// identifier — an unnamed parameter (NewUnnamed) and a blank-identifier
// parameter (NewBlank). Both must be flagged (closes the anonymous-param
// bypass). NewNamed is the negative control (named + guarded → no fire).
// Loaded only via Run(t, Fixture(...)).
package redanonymousparam

import "github.com/ghbvf/gocell/pkg/validation"

// MyInterface is a minimal interface for fixture purposes.
type MyInterface interface {
	Do() error
}

// NewUnnamed has an unnamed interface parameter — cannot be guarded → fires.
func NewUnnamed(MyInterface) *struct{} {
	return &struct{}{}
}

// NewBlank has a blank-identifier interface parameter — cannot be guarded → fires.
func NewBlank(_ MyInterface) *struct{} {
	return &struct{}{}
}

// NewNamed is the negative control: named + guarded → must NOT fire.
func NewNamed(dep MyInterface) (*struct{}, error) {
	if validation.IsNilInterface(dep) {
		return nil, nil
	}
	return &struct{}{}, nil
}
