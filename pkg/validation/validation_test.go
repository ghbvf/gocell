package validation_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// assertValidationResult is a t.Helper that validates the outcome of a
// RequireNotBlank call. When wantMessage is empty it asserts nil error;
// otherwise it asserts a *errcode.Error with the expected code and message.
func assertValidationResult(t *testing.T, err error, wantCode errcode.Code, wantMessage string) {
	t.Helper()
	if wantMessage == "" {
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error with message %q, got nil", wantMessage)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != wantCode {
		t.Errorf("code = %q, want %q", ec.Code, wantCode)
	}
	if ec.Message != wantMessage {
		t.Errorf("message = %q, want %q", ec.Message, wantMessage)
	}
}

// All fields in a single RequireNotBlank call share one errcode by design —
// each slice domain has one validation code (ErrAuthIdentityInvalidInput,
// ErrConfigInvalidInput, etc.), and the helper preserves that classification.
// TestRequireNotBlank_PreservesCallerCode covers the multi-code dimension.
func TestRequireNotBlank(t *testing.T) {
	t.Parallel()

	const code = errcode.ErrAuthIdentityInvalidInput

	tests := []struct {
		name        string
		fields      []validation.NamedValue
		wantCode    errcode.Code
		wantMessage string
	}{
		{
			name:   "single field non-blank returns nil",
			fields: []validation.NamedValue{validation.F("id", "u1")},
		},
		{
			name:        "single field blank returns required error",
			fields:      []validation.NamedValue{validation.F("id", "")},
			wantCode:    code,
			wantMessage: "id is required",
		},
		{
			name: "multi field all non-blank returns nil",
			fields: []validation.NamedValue{
				validation.F("id", "u1"),
				validation.F("name", "alice"),
				validation.F("email", "a@b.com"),
			},
		},
		{
			name: "multi field first blank reports first",
			fields: []validation.NamedValue{
				validation.F("id", ""),
				validation.F("name", ""),
			},
			wantCode:    code,
			wantMessage: "id is required",
		},
		{
			name: "multi field second blank reports second",
			fields: []validation.NamedValue{
				validation.F("id", "u1"),
				validation.F("name", ""),
			},
			wantCode:    code,
			wantMessage: "name is required",
		},
		{
			name:   "whitespace-only value passes (not trimmed)",
			fields: []validation.NamedValue{validation.F("id", "  ")},
		},
		{
			name:   "zero fields returns nil",
			fields: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validation.RequireNotBlank(code, tt.fields...)
			assertValidationResult(t, err, tt.wantCode, tt.wantMessage)
		})
	}
}

func TestRequireNotBlank_PreservesCallerCode(t *testing.T) {
	t.Parallel()

	cases := []errcode.Code{
		errcode.ErrValidationFailed,
		errcode.ErrAuthIdentityInvalidInput,
		errcode.ErrAuthLoginInvalidInput,
		errcode.ErrConfigInvalidInput,
		errcode.ErrFlagInvalidInput,
	}

	for _, code := range cases {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			err := validation.RequireNotBlank(code, validation.F("x", ""))
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("expected *errcode.Error, got %T", err)
			}
			if ec.Code != code {
				t.Errorf("code = %q, want %q", ec.Code, code)
			}
		})
	}
}
