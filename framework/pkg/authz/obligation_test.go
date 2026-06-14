package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// TestValidAttributeKey covers the canonical attribute/column identifier
// validation introduced by F5 (#1344 PR-6 review).
func TestValidAttributeKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid identifiers
		{"simple ascii", "department", false},
		{"with underscore", "owner_id", false},
		{"with dot", "device.compliant", false},
		{"with digits after first char", "field1", false},
		{"mixed case", "myField", false},
		{"all caps", "SSN", false},
		{"single char", "x", false},
		// Invalid: empty
		{"empty string", "", true},
		// Invalid: leading whitespace
		{"leading space", " ssn", true},
		// Invalid: trailing whitespace
		{"trailing newline", "ssn\n", true},
		// Invalid: internal whitespace
		{"internal space", "my field", true},
		// Invalid: starts with digit
		{"starts with digit", "1abc", true},
		// Invalid: starts with underscore
		{"starts with underscore", "_field", true},
		// Invalid: control character
		{"tab char", "field\t", true},
		// Invalid: starts with dot
		{"starts with dot", ".field", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidAttributeKey(tc.input)
			if tc.wantErr {
				require.Error(t, err, "ValidAttributeKey(%q) should return error", tc.input)
			} else {
				require.NoError(t, err, "ValidAttributeKey(%q) should not return error", tc.input)
			}
		})
	}
}

func TestFieldMask_IsZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fm   FieldMask
		want bool
	}{
		{"empty is zero", FieldMask{}, true},
		{"nil slice is zero", FieldMask{Fields: nil}, true},
		{"one field is not zero", FieldMask{Fields: []string{"id"}}, false},
		{"two fields are not zero", FieldMask{Fields: []string{"email", "phone"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.fm.IsZero())
		})
	}
}

func TestFieldMask_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fm      FieldMask
		wantErr bool
	}{
		{
			name:    "empty fields is valid (identity projection)",
			fm:      FieldMask{},
			wantErr: false,
		},
		{
			name:    "nil fields is valid",
			fm:      FieldMask{Fields: nil},
			wantErr: false,
		},
		{
			name:    "one valid field",
			fm:      FieldMask{Fields: []string{"email"}},
			wantErr: false,
		},
		{
			name:    "two distinct valid fields",
			fm:      FieldMask{Fields: []string{"email", "phone"}},
			wantErr: false,
		},
		{
			name:    "empty string entry rejected",
			fm:      FieldMask{Fields: []string{"email", ""}},
			wantErr: true,
		},
		{
			name:    "sole empty string entry rejected",
			fm:      FieldMask{Fields: []string{""}},
			wantErr: true,
		},
		{
			name:    "duplicate rejected",
			fm:      FieldMask{Fields: []string{"email", "phone", "email"}},
			wantErr: true,
		},
		{
			name:    "duplicate adjacent rejected",
			fm:      FieldMask{Fields: []string{"id", "id"}},
			wantErr: true,
		},
		// F5: whitespace/control char keys must be rejected by ValidAttributeKey.
		{
			name:    "leading-space key rejected",
			fm:      FieldMask{Fields: []string{" ssn"}},
			wantErr: true,
		},
		{
			name:    "trailing-newline key rejected",
			fm:      FieldMask{Fields: []string{"ssn\n"}},
			wantErr: true,
		},
		{
			name:    "starts-with-digit key rejected",
			fm:      FieldMask{Fields: []string{"1abc"}},
			wantErr: true,
		},
		{
			name:    "valid dot-separated key",
			fm:      FieldMask{Fields: []string{"device.compliant"}},
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.fm.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestObligations_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		o       Obligations
		wantErr bool
	}{
		{
			name:    "zero Obligations is valid (policy does not impose a row-scope constraint)",
			o:       Obligations{},
			wantErr: false,
		},
		{
			name:    "valid non-zero RowScope with empty FieldMask",
			o:       Obligations{RowScope: tenant.RowScopeSelf},
			wantErr: false,
		},
		{
			name:    "valid RowScopeTenant with valid FieldMask",
			o:       Obligations{RowScope: tenant.RowScopeTenant, FieldMask: FieldMask{Fields: []string{"ssn"}}},
			wantErr: false,
		},
		{
			name:    "invalid out-of-range RowScope rejected",
			o:       Obligations{RowScope: tenant.RowScope(99)},
			wantErr: true,
		},
		{
			name:    "zero RowScope with invalid FieldMask (empty string)",
			o:       Obligations{FieldMask: FieldMask{Fields: []string{""}}},
			wantErr: true,
		},
		{
			name:    "zero RowScope with duplicate FieldMask",
			o:       Obligations{FieldMask: FieldMask{Fields: []string{"col", "col"}}},
			wantErr: true,
		},
		{
			name:    "valid RowScopeAll, no FieldMask",
			o:       Obligations{RowScope: tenant.RowScopeAll},
			wantErr: false,
		},
		{
			name:    "RowScope zero is explicitly allowed (not validated when zero)",
			o:       Obligations{RowScope: 0, FieldMask: FieldMask{Fields: []string{"col1", "col2"}}},
			wantErr: false,
		},
		{
			name: "non-zero valid RowScope with invalid FieldMask returns FieldMask error",
			o: Obligations{
				RowScope:  tenant.RowScopeAll,
				FieldMask: FieldMask{Fields: []string{""}},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.o.Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestFieldMask_Masks covers the obligation-query a PEP uses to decide whether a
// column is governed by the mask (e.g. to reject a masked-column query predicate).
func TestFieldMask_Masks(t *testing.T) {
	t.Parallel()
	fm := FieldMask{Fields: []string{"traceId", "correlationId"}}
	assert.True(t, fm.Masks("traceId"), "listed column is masked")
	assert.True(t, fm.Masks("correlationId"))
	assert.False(t, fm.Masks("subjectId"), "unlisted column is not masked")
	assert.False(t, fm.Masks(""), "empty column is never masked")
	// A zero / identity mask masks nothing.
	assert.False(t, FieldMask{}.Masks("traceId"))
	assert.False(t, IdentityFieldMask().Masks("traceId"))
}

// TestIdentityFieldMask asserts the named identity marker is the empty mask
// (masks nothing) and is interchangeable with a bare FieldMask{}.
func TestIdentityFieldMask(t *testing.T) {
	t.Parallel()
	fm := IdentityFieldMask()
	assert.True(t, fm.IsZero(), "identity mask masks nothing")
	assert.NoError(t, fm.Validate(), "identity mask is a valid obligation")
}
