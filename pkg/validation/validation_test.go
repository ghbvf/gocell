package validation_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

type nilInterfaceSample struct{}

type nilInterfaceMarker interface{ Marker() }

type nilInterfaceImpl struct{}

func (*nilInterfaceImpl) Marker() {}

// assertValidationResult is a t.Helper that validates the outcome of a
// RequireNotEmpty call. When wantField is empty it asserts nil error;
// otherwise it asserts a *errcode.Error with the expected code, const message,
// and a Details entry with key "field" matching wantField.
func assertValidationResult(t *testing.T, err error, wantCode errcode.Code, wantField string) {
	t.Helper()
	if wantField == "" {
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error with field %q in details, got nil", wantField)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != wantCode {
		t.Errorf("code = %q, want %q", ec.Code, wantCode)
	}
	const wantMsg = "validation: required field missing"
	if ec.Message != wantMsg {
		t.Errorf("message = %q, want %q", ec.Message, wantMsg)
	}
	var foundField string
	for _, attr := range ec.Details {
		if attr.Key == "field" {
			foundField = attr.Value.String()
			break
		}
	}
	if foundField != wantField {
		t.Errorf("details[field] = %q, want %q", foundField, wantField)
	}
}

func TestIsNilInterface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want bool
	}{
		{name: "bare nil", in: nil, want: true},
		{name: "typed nil pointer", in: (*nilInterfaceSample)(nil), want: true},
		{name: "typed nil map", in: map[string]string(nil), want: true},
		{name: "typed nil slice", in: []string(nil), want: true},
		{name: "typed nil func", in: (func())(nil), want: true},
		{name: "non nil pointer", in: &nilInterfaceSample{}, want: false},
		{name: "non nil map", in: map[string]string{}, want: false},
		{name: "non nil slice", in: []string{}, want: false},
		{name: "non nil string", in: "x", want: false},
		{name: "zero struct", in: nilInterfaceSample{}, want: false},
	}

	var typedNil nilInterfaceMarker = (*nilInterfaceImpl)(nil)
	tests = append(tests, struct {
		name string
		in   any
		want bool
	}{name: "typed nil interface", in: typedNil, want: true})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := validation.IsNilInterface(tt.in); got != tt.want {
				t.Fatalf("IsNilInterface() = %v, want %v", got, tt.want)
			}
		})
	}
}

// All fields in a single RequireNotEmpty call share one errcode by design —
// each slice domain has one validation code (ErrAuthIdentityInvalidInput,
// ErrConfigInvalidInput, etc.), and the helper preserves that classification.
// TestRequireNotEmpty_PreservesCallerCode covers the multi-code dimension.
func TestRequireNotEmpty(t *testing.T) {
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
			wantMessage: "id",
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
			wantMessage: "id",
		},
		{
			name: "multi field second blank reports second",
			fields: []validation.NamedValue{
				validation.F("id", "u1"),
				validation.F("name", ""),
			},
			wantCode:    code,
			wantMessage: "name",
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
			err := validation.RequireNotEmpty(code, tt.fields...)
			assertValidationResult(t, err, tt.wantCode, tt.wantMessage)
		})
	}
}

func TestRequireNotEmpty_PreservesCallerCode(t *testing.T) {
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
			err := validation.RequireNotEmpty(code, validation.F("x", ""))
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
