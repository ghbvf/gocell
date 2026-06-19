package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

func makeValidRuleWithFieldMask(id string) abac.Rule {
	r := makeValidRule(id)
	r.Obligations.FieldMask.Fields = []string{"email", "phone"}
	return r
}

const (
	validTenantID1 = tenant.TenantID("11111111-1111-1111-1111-111111111111")
	validTenantID2 = tenant.TenantID("22222222-2222-2222-2222-222222222222")
)

func makeValidRule(id string) abac.Rule {
	return abac.Rule{
		ID:     id,
		Name:   "rule " + id,
		Effect: authz.EffectAllow,
		// #1979: an allow rule must declare at least one Action.
		Action: []string{"user:read"},
		Conditions: []abac.Condition{
			{
				Source:   abac.SourceSubject,
				Key:      "department",
				Operator: abac.OpEquals,
				Values:   []string{"eng"},
			},
		},
	}
}

func makeValidPolicy() *abac.Policy {
	return &abac.Policy{
		ID:       "policy-1",
		TenantID: validTenantID1,
		Name:     "Engineering Policy",
		Rules:    []abac.Rule{makeValidRule("rule-1")},
	}
}

// TestPolicy_Clone proves that Policy.Clone returns a deep-independent copy:
// mutations to the clone do not affect the original, and vice versa, including
// the FieldMask.Fields slice (the known shallow-copy regression from PR-6 F1).
func TestPolicy_Clone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		orig   *abac.Policy
		mutate func(clone *abac.Policy)
		check  func(t *testing.T, orig *abac.Policy)
	}{
		{
			name: "scalar fields are independent",
			orig: makeValidPolicy(),
			mutate: func(c *abac.Policy) {
				c.Name = "mutated"
				c.Description = "mutated"
				c.Version = 99
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Name == "mutated" {
					t.Errorf("Clone Name mutation leaked into original")
				}
				if orig.Version == 99 {
					t.Errorf("Clone Version mutation leaked into original")
				}
			},
		},
		{
			name: "Rules slice is independent",
			orig: makeValidPolicy(),
			mutate: func(c *abac.Policy) {
				c.Rules[0].Name = "mutated-rule"
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Rules[0].Name == "mutated-rule" {
					t.Errorf("Clone Rules[0].Name mutation leaked into original")
				}
			},
		},
		{
			name: "Conditions slice is independent",
			orig: makeValidPolicy(),
			mutate: func(c *abac.Policy) {
				c.Rules[0].Conditions[0].Key = "mutated-key"
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Rules[0].Conditions[0].Key == "mutated-key" {
					t.Errorf("Clone Conditions[0].Key mutation leaked into original")
				}
			},
		},
		{
			name: "Values slice is independent",
			orig: makeValidPolicy(),
			mutate: func(c *abac.Policy) {
				c.Rules[0].Conditions[0].Values[0] = "mutated-value"
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Rules[0].Conditions[0].Values[0] == "mutated-value" {
					t.Errorf("Clone Values[0] mutation leaked into original")
				}
			},
		},
		{
			name: "FieldMask.Fields slice is independent (regression: shallow-copy PR-6 F1)",
			orig: func() *abac.Policy {
				p := &abac.Policy{
					ID:       "pol-fm",
					TenantID: validTenantID1,
					Name:     "FieldMaskPolicy",
					Rules:    []abac.Rule{makeValidRuleWithFieldMask("r1")},
				}
				return p
			}(),
			mutate: func(c *abac.Policy) {
				c.Rules[0].Obligations.FieldMask.Fields[0] = "mutated-field"
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Rules[0].Obligations.FieldMask.Fields[0] == "mutated-field" {
					t.Errorf("Clone FieldMask.Fields[0] mutation leaked into original (shallow-copy regression)")
				}
			},
		},
		{
			name: "Action slice is independent (PR-10a #1348 — F4 deep-copy)",
			orig: func() *abac.Policy {
				p := makeValidPolicy()
				p.Rules[0].Action = []string{"audit:read"}
				return p
			}(),
			mutate: func(c *abac.Policy) {
				c.Rules[0].Action[0] = "mutated:action"
			},
			check: func(t *testing.T, orig *abac.Policy) {
				if orig.Rules[0].Action[0] == "mutated:action" {
					t.Errorf("Clone Action[0] mutation leaked into original (shallow-copy)")
				}
			},
		},
		{
			name:   "original mutation does not affect clone",
			orig:   makeValidPolicy(),
			mutate: func(_ *abac.Policy) {},
			check: func(t *testing.T, orig *abac.Policy) {
				// This test verifies the clone is taken before the mutation.
				// We take a clone and mutate the original, then re-check separately.
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clone := tc.orig.Clone()
			if clone == tc.orig {
				t.Fatal("Clone() returned the same pointer as original")
			}
			tc.mutate(clone)
			tc.check(t, tc.orig)
		})
	}

	// Additional check: mutate original AFTER cloning; clone must be unaffected.
	t.Run("original_mutation_after_clone_does_not_affect_clone", func(t *testing.T) {
		t.Parallel()
		orig := makeValidPolicy()
		clone := orig.Clone()
		orig.Name = "mutated-original"
		orig.Rules[0].Name = "mutated-rule"
		orig.Rules[0].Conditions[0].Key = "mutated-key"
		orig.Rules[0].Conditions[0].Values[0] = "mutated-value"
		if clone.Name == "mutated-original" {
			t.Errorf("Clone.Name was affected by original mutation")
		}
		if clone.Rules[0].Name == "mutated-rule" {
			t.Errorf("Clone.Rules[0].Name was affected by original mutation")
		}
		if clone.Rules[0].Conditions[0].Key == "mutated-key" {
			t.Errorf("Clone.Conditions[0].Key was affected by original mutation")
		}
		if clone.Rules[0].Conditions[0].Values[0] == "mutated-value" {
			t.Errorf("Clone.Values[0] was affected by original mutation")
		}
	})
}

func TestPolicy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  *abac.Policy
		wantErr bool
	}{
		{
			name:    "valid policy",
			policy:  makeValidPolicy(),
			wantErr: false,
		},
		{
			name: "empty ID",
			policy: &abac.Policy{
				ID:       "",
				TenantID: validTenantID1,
				Name:     "Test",
				Rules:    []abac.Rule{makeValidRule("r1")},
			},
			wantErr: true,
		},
		{
			name: "empty TenantID",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: tenant.TenantID(""),
				Name:     "Test",
				Rules:    []abac.Rule{makeValidRule("r1")},
			},
			wantErr: true,
		},
		{
			name: "non-canonical TenantID",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: tenant.TenantID("not-a-uuid"),
				Name:     "Test",
				Rules:    []abac.Rule{makeValidRule("r1")},
			},
			wantErr: true,
		},
		{
			name: "empty Name",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "",
				Rules:    []abac.Rule{makeValidRule("r1")},
			},
			wantErr: true,
		},
		{
			name: "zero rules",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "Empty",
				Rules:    []abac.Rule{},
			},
			wantErr: true,
		},
		{
			name: "nil rules",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "Empty",
				Rules:    nil,
			},
			wantErr: true,
		},
		{
			name: "invalid nested rule",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "Bad rule",
				Rules: []abac.Rule{
					{ID: "", Name: "bad", Effect: authz.EffectAllow, Conditions: nil},
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate rule IDs",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "Dup",
				Rules: []abac.Rule{
					makeValidRule("rule-dup"),
					makeValidRule("rule-dup"),
				},
			},
			wantErr: true,
		},
		{
			name: "multiple rules with unique IDs",
			policy: &abac.Policy{
				ID:       "p1",
				TenantID: validTenantID1,
				Name:     "Multi-rule",
				Rules: []abac.Rule{
					makeValidRule("rule-1"),
					makeValidRule("rule-2"),
					makeValidRule("rule-3"),
				},
			},
			wantErr: false,
		},
		{
			name: "description optional",
			policy: &abac.Policy{
				ID:          "p1",
				TenantID:    validTenantID1,
				Name:        "Test",
				Description: "optional description",
				Rules:       []abac.Rule{makeValidRule("r1")},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Policy.Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
