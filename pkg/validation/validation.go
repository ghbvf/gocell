// Package validation provides field-level input validation helpers that
// return structured errcode errors suitable for HTTP handlers and service-
// layer preconditions.
//
// Depends only on stdlib + pkg/errcode to respect the pkg/ layering rule.
package validation

import (
	"fmt"
	"reflect"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// NamedValue pairs a field name with its current value for the Require*
// helpers. Construct with F() for readable call sites.
type NamedValue struct {
	Name  string
	Value string
}

// F is shorthand for NamedValue{Name: name, Value: value}.
func F(name, value string) NamedValue {
	return NamedValue{Name: name, Value: value}
}

// RequireNotEmpty returns nil when every field's value is non-empty. On
// the first empty value it returns errcode.New(code, "<field> is required").
//
// Empty means value == ""; whitespace-only strings are NOT trimmed, matching
// the existing == "" convention across the repo. Callers choose the code
// to preserve domain-specific classification (ErrAuthIdentityInvalidInput,
// ErrConfigInvalidInput, etc.).
//
// Usage:
//
//	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
//	    validation.F("id", req.ID),
//	); err != nil {
//	    return err
//	}
//
//	if err := validation.RequireNotEmpty(errcode.ErrAuthLoginInvalidInput,
//	    validation.F("userId", req.UserID),
//	    validation.F("oldPassword", req.OldPassword),
//	    validation.F("newPassword", req.NewPassword),
//	); err != nil {
//	    return err
//	}
func RequireNotEmpty(code errcode.Code, fields ...NamedValue) error {
	for _, f := range fields {
		if f.Value == "" {
			return errcode.New(errcode.KindInvalid, code, fmt.Sprintf("%s is required", f.Name))
		}
	}
	return nil
}

// IsNilInterface reports whether v is nil or a typed-nil interface value.
//
// Use this at construction boundaries for interface dependencies. A plain
// `dep == nil` check misses values such as `var dep *Repo; New(dep)` because
// the interface carries a non-nil concrete type descriptor.
func IsNilInterface(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
