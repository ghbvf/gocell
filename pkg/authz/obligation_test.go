package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/tenant"
)

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
			name:    "zero Obligations is valid (engine derives default in PR-5)",
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
