package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
)

func TestOperator_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		op    abac.Operator
		valid bool
	}{
		{"zero value invalid", 0, false},
		{"OpEquals valid", abac.OpEquals, true},
		{"OpNotEquals valid", abac.OpNotEquals, true},
		{"OpIn valid", abac.OpIn, true},
		{"OpNotIn valid", abac.OpNotIn, true},
		{"out of range invalid", abac.Operator(99), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.op.Valid(); got != tc.valid {
				t.Errorf("Operator(%d).Valid() = %v, want %v", tc.op, got, tc.valid)
			}
		})
	}
}

func TestOperator_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		op   abac.Operator
		want string
	}{
		{0, "invalid"},
		{abac.OpEquals, "eq"},
		{abac.OpNotEquals, "neq"},
		{abac.OpIn, "in"},
		{abac.OpNotIn, "not_in"},
		{abac.Operator(99), "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.op.String(); got != tc.want {
				t.Errorf("Operator(%d).String() = %q, want %q", tc.op, got, tc.want)
			}
		})
	}
}

func TestOperator_Validate(t *testing.T) {
	t.Parallel()

	validOps := []abac.Operator{abac.OpEquals, abac.OpNotEquals, abac.OpIn, abac.OpNotIn}
	for _, op := range validOps {
		t.Run(op.String(), func(t *testing.T) {
			t.Parallel()
			if err := op.Validate(); err != nil {
				t.Errorf("Operator(%d).Validate() = %v, want nil", op, err)
			}
		})
	}

	invalidOps := []abac.Operator{0, abac.Operator(99)}
	for _, op := range invalidOps {
		t.Run("invalid_"+op.String(), func(t *testing.T) {
			t.Parallel()
			if err := op.Validate(); err == nil {
				t.Errorf("Operator(%d).Validate() = nil, want error", op)
			}
		})
	}
}
