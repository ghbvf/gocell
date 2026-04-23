// Package validation provides field-level input validation helpers that
// return structured errcode errors suitable for HTTP handlers and service-
// layer preconditions.
//
// Depends only on stdlib + pkg/errcode to respect the pkg/ layering rule.
package validation

import (
	"fmt"

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

// RequireNotBlank returns nil when every field's value is non-empty. On
// the first empty value it returns errcode.New(code, "<field> is required").
//
// Empty means value == ""; whitespace-only strings are NOT trimmed, matching
// the existing == "" convention across the repo. Callers choose the code
// to preserve domain-specific classification (ErrAuthIdentityInvalidInput,
// ErrConfigInvalidInput, etc.).
//
// Usage:
//
//	if err := validation.RequireNotBlank(errcode.ErrAuthIdentityInvalidInput,
//	    validation.F("id", req.ID),
//	); err != nil {
//	    return err
//	}
//
//	if err := validation.RequireNotBlank(errcode.ErrAuthLoginInvalidInput,
//	    validation.F("userId", req.UserID),
//	    validation.F("oldPassword", req.OldPassword),
//	    validation.F("newPassword", req.NewPassword),
//	); err != nil {
//	    return err
//	}
func RequireNotBlank(code errcode.Code, fields ...NamedValue) error {
	for _, f := range fields {
		if f.Value == "" {
			return errcode.New(code, fmt.Sprintf("%s is required", f.Name))
		}
	}
	return nil
}
