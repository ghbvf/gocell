package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
)

func TestAttributeSource_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		src   abac.AttributeSource
		valid bool
	}{
		{"zero value invalid", 0, false},
		{"SourceSubject valid", abac.SourceSubject, true},
		{"SourceResource valid", abac.SourceResource, true},
		{"SourceEnvironment valid", abac.SourceEnvironment, true},
		{"out of range invalid", abac.AttributeSource(99), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.src.Valid(); got != tc.valid {
				t.Errorf("AttributeSource(%d).Valid() = %v, want %v", tc.src, got, tc.valid)
			}
		})
	}
}

func TestAttributeSource_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		src  abac.AttributeSource
		want string
	}{
		{0, "invalid"},
		{abac.SourceSubject, "subject"},
		{abac.SourceResource, "resource"},
		{abac.SourceEnvironment, "environment"},
		{abac.AttributeSource(99), "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.src.String(); got != tc.want {
				t.Errorf("AttributeSource(%d).String() = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestCondition_Validate(t *testing.T) {
	t.Parallel()

	good := abac.Condition{
		Source:   abac.SourceSubject,
		Key:      "department",
		Operator: abac.OpEquals,
		Values:   []string{"eng"},
	}

	tests := []struct {
		name    string
		cond    abac.Condition
		wantErr bool
	}{
		{
			name:    "valid condition",
			cond:    good,
			wantErr: false,
		},
		{
			name:    "zero Source",
			cond:    abac.Condition{Source: 0, Key: "dept", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "invalid Source",
			cond:    abac.Condition{Source: abac.AttributeSource(99), Key: "dept", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "empty Key",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "zero Operator",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "dept", Operator: 0, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "invalid Operator",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "dept", Operator: abac.Operator(99), Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "empty Values slice",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "dept", Operator: abac.OpEquals, Values: []string{}},
			wantErr: true,
		},
		{
			name:    "nil Values",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "dept", Operator: abac.OpEquals, Values: nil},
			wantErr: true,
		},
		{
			name:    "Values contains empty string",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "dept", Operator: abac.OpIn, Values: []string{"eng", ""}},
			wantErr: true,
		},
		{
			name:    "OpIn with multiple values valid",
			cond:    abac.Condition{Source: abac.SourceResource, Key: "classification", Operator: abac.OpIn, Values: []string{"public", "internal"}},
			wantErr: false,
		},
		{
			name:    "SourceEnvironment valid",
			cond:    abac.Condition{Source: abac.SourceEnvironment, Key: "time_of_day", Operator: abac.OpNotEquals, Values: []string{"night"}},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cond.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Condition.Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
