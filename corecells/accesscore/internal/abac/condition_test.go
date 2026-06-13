package abac_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
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

func TestAttributeSource_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     abac.AttributeSource
		wantErr bool
	}{
		{"zero invalid", 0, true},
		{"SourceSubject valid", abac.SourceSubject, false},
		{"SourceResource valid", abac.SourceResource, false},
		{"SourceEnvironment valid", abac.SourceEnvironment, false},
		{"out of range invalid", abac.AttributeSource(99), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.src.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("AttributeSource(%d).Validate() error = %v, wantErr %v", tc.src, err, tc.wantErr)
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

func TestParseAttributeSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    abac.AttributeSource
		wantErr bool
	}{
		// valid codes round-trip with String()
		{"subject round-trips", "subject", abac.SourceSubject, false},
		{"resource round-trips", "resource", abac.SourceResource, false},
		{"environment round-trips", "environment", abac.SourceEnvironment, false},
		// unknown codes → error (fail-closed)
		{"empty string unknown", "", 0, true},
		{"action unknown", "action", 0, true},
		{"SUBJECT uppercase unknown", "SUBJECT", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := abac.ParseAttributeSource(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.Zero(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				assert.Equal(t, tc.input, got.String(), "ParseAttributeSource → String() must be identity")
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
		// F6: canonical key validation via authz.ValidAttributeKey.
		{
			name:    "Key with leading space rejected",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: " department", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "Key with trailing newline rejected",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "department\n", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "Key starting with digit rejected",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "1level", Operator: abac.OpEquals, Values: []string{"eng"}},
			wantErr: true,
		},
		{
			name:    "Key with control character rejected",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "field\x00name", Operator: abac.OpEquals, Values: []string{"v"}},
			wantErr: true,
		},
		{
			name:    "dot-separated Key valid",
			cond:    abac.Condition{Source: abac.SourceResource, Key: "device.compliant", Operator: abac.OpEquals, Values: []string{"true"}},
			wantErr: false,
		},
		{
			name:    "underscore Key valid",
			cond:    abac.Condition{Source: abac.SourceSubject, Key: "job_level", Operator: abac.OpEquals, Values: []string{"L5"}},
			wantErr: false,
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
		// Cross-attribute operator (OpEqualsAttr): RHS is an attribute reference
		// (RHSSource, RHSKey), NOT static Values. validateRHS makes mixing the two
		// shapes unexpressible (#1977).
		{
			name: "valid eq_attr (subject.sub == resource.id)",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: "id",
			},
			wantErr: false,
		},
		{
			name: "eq_attr carrying stray static Values rejected",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: "id", Values: []string{"x"},
			},
			wantErr: true,
		},
		{
			name: "eq_attr with zero RHSSource rejected",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: 0, RHSKey: "id",
			},
			wantErr: true,
		},
		{
			name: "eq_attr with empty RHSKey rejected",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: "",
			},
			wantErr: true,
		},
		{
			name: "eq_attr with invalid RHSKey rejected",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "sub", Operator: abac.OpEqualsAttr,
				RHSSource: abac.SourceResource, RHSKey: " id",
			},
			wantErr: true,
		},
		{
			name: "static operator carrying stray RHS reference rejected",
			cond: abac.Condition{
				Source: abac.SourceSubject, Key: "dept", Operator: abac.OpEquals,
				Values: []string{"eng"}, RHSSource: abac.SourceResource, RHSKey: "id",
			},
			wantErr: true,
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
