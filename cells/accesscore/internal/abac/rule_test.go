package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/tenant"
)

func TestRule_Validate(t *testing.T) {
	t.Parallel()

	goodCond := abac.Condition{
		Source:   abac.SourceSubject,
		Key:      "department",
		Operator: abac.OpEquals,
		Values:   []string{"eng"},
	}
	goodRule := abac.Rule{
		ID:         "rule-1",
		Name:       "Engineering Access",
		Effect:     authz.EffectAllow,
		Conditions: []abac.Condition{goodCond},
	}

	tests := []struct {
		name    string
		rule    abac.Rule
		wantErr bool
	}{
		{
			name:    "valid rule with condition",
			rule:    goodRule,
			wantErr: false,
		},
		{
			name: "valid rule with zero conditions (allowed)",
			rule: abac.Rule{
				ID:         "rule-no-cond",
				Name:       "Unrestricted",
				Effect:     authz.EffectDeny,
				Conditions: []abac.Condition{},
			},
			wantErr: false,
		},
		{
			name: "empty ID",
			rule: abac.Rule{
				ID:         "",
				Name:       "Test",
				Effect:     authz.EffectAllow,
				Conditions: nil,
			},
			wantErr: true,
		},
		{
			name: "empty Name",
			rule: abac.Rule{
				ID:         "rule-x",
				Name:       "",
				Effect:     authz.EffectAllow,
				Conditions: nil,
			},
			wantErr: true,
		},
		{
			name: "zero Effect",
			rule: abac.Rule{
				ID:         "rule-x",
				Name:       "Test",
				Effect:     0,
				Conditions: nil,
			},
			wantErr: true,
		},
		{
			name: "invalid Effect",
			rule: abac.Rule{
				ID:         "rule-x",
				Name:       "Test",
				Effect:     authz.Effect(99),
				Conditions: nil,
			},
			wantErr: true,
		},
		{
			name: "invalid condition propagates",
			rule: abac.Rule{
				ID:     "rule-x",
				Name:   "Test",
				Effect: authz.EffectAllow,
				Conditions: []abac.Condition{
					{Source: 0, Key: "dept", Operator: abac.OpEquals, Values: []string{"eng"}},
				},
			},
			wantErr: true,
		},
		{
			name: "deny rule with valid obligations",
			rule: abac.Rule{
				ID:         "deny-rule",
				Name:       "Deny sensitive",
				Effect:     authz.EffectDeny,
				Conditions: []abac.Condition{goodCond},
			},
			wantErr: false,
		},
		{
			name: "invalid Obligations propagates",
			rule: abac.Rule{
				ID:          "rule-x",
				Name:        "Test",
				Effect:      authz.EffectAllow,
				Obligations: authz.Obligations{RowScope: tenant.RowScope(99)},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.rule.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Rule.Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
