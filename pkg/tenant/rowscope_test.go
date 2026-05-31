package tenant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRowScope_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   RowScope
		want bool
	}{
		{"zero value invalid", RowScope(0), false},
		{"self valid", RowScopeSelf, true},
		{"device valid", RowScopeDevice, true},
		{"tenant valid", RowScopeTenant, true},
		{"all valid", RowScopeAll, true},
		{"out of range invalid", RowScope(99), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.in.Valid())
		})
	}
}

func TestRowScope_Validate(t *testing.T) {
	t.Parallel()
	assert.Error(t, RowScope(0).Validate(), "zero value must fail Validate")
	assert.Error(t, RowScope(99).Validate(), "out of range must fail Validate")
	for _, rs := range []RowScope{RowScopeSelf, RowScopeDevice, RowScopeTenant, RowScopeAll} {
		assert.NoError(t, rs.Validate())
	}
}

func TestRowScope_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   RowScope
		want string
	}{
		{RowScopeSelf, "self"},
		{RowScopeDevice, "device"},
		{RowScopeTenant, "tenant"},
		{RowScopeAll, "all"},
		{RowScope(0), "invalid"},
		{RowScope(99), "invalid"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, tc.in.String())
	}
}
