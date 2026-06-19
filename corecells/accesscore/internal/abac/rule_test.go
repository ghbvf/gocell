package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
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
		Action:     []string{"user:read"},
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
			// Deny is the reason this is valid: a deny rule with zero conditions
			// AND empty Action is a legitimate deny-all. The same shape with
			// EffectAllow would be rejected (#1979 — see "allow rule with empty
			// Action rejected" below).
			name: "deny rule with zero conditions and empty Action is valid (deny-all)",
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
			// PR #2077 F1: tenant rule ID must not start with "_" — that prefix is
			// reserved for the PDP's framework decision-attribution sentinels
			// (_default-deny / _invalid-obligations); a collision would make
			// matched_rule_id attribution ambiguous/spoofable.
			name: "reserved _ prefix ID (sentinel collision) rejected",
			rule: abac.Rule{
				ID:         "_default-deny",
				Name:       "Sneaky tenant rule",
				Effect:     authz.EffectAllow,
				Conditions: nil,
			},
			wantErr: true,
		},
		{
			name: "reserved _ prefix ID (arbitrary) rejected",
			rule: abac.Rule{
				ID:         "_my-rule",
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
				Action: []string{"x:y"},
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
				Action:      []string{"x:y"},
				Obligations: authz.Obligations{RowScope: tenant.RowScope(99)},
			},
			wantErr: true,
		},
		{
			// #1979: an allow rule with empty Action would (combined with empty
			// Conditions) unconditionally permit every action — blowing open the
			// route gate for all permissions. Allow rules MUST name at least one
			// Action. Rejected even when Conditions are present (a condition-scoped
			// blanket allow still widens every route gate it does not exclude).
			name: "allow rule with empty Action rejected",
			rule: abac.Rule{
				ID:         "allow-no-action",
				Name:       "Blanket allow",
				Effect:     authz.EffectAllow,
				Conditions: []abac.Condition{goodCond},
			},
			wantErr: true,
		},
		{
			name: "allow rule with non-empty Action passes",
			rule: abac.Rule{
				ID:     "allow-scoped",
				Name:   "Scoped allow",
				Effect: authz.EffectAllow,
				Action: []string{"audit:read"},
			},
			wantErr: false,
		},
		{
			// Deny with empty Action stays valid: an untargeted deny is a legitimate
			// deny-all (forbid-wins over every action). Only Allow requires a target.
			name: "deny rule with empty Action allowed (deny-all)",
			rule: abac.Rule{
				ID:     "deny-all",
				Name:   "Deny everything",
				Effect: authz.EffectDeny,
			},
			wantErr: false,
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

// TestRule_ValidateStored covers the stored-read profile (#2409 F1). It differs
// from Validate (authoring) in exactly ONE case: an empty-Action Allow is tolerated
// (a legacy persisted row the evaluator renders inert), NOT rejected. Every genuine
// structural-integrity violation must STILL be rejected — that is the anti-vacuity
// half that keeps the tolerance scoped, not a blanket "accept anything".
func TestRule_ValidateStored(t *testing.T) {
	t.Parallel()

	goodCond := abac.Condition{Source: abac.SourceSubject, Key: "department", Operator: abac.OpEquals, Values: []string{"eng"}}

	tests := []struct {
		name    string
		rule    abac.Rule
		wantErr bool
	}{
		{
			// THE differentiator: rejected by Validate (authoring), tolerated here.
			name:    "empty-Action allow tolerated (legacy persisted, inert in evaluator)",
			rule:    abac.Rule{ID: "legacy-allow", Name: "Untargeted allow", Effect: authz.EffectAllow},
			wantErr: false,
		},
		{
			name:    "empty-Action allow with conditions also tolerated",
			rule:    abac.Rule{ID: "legacy-allow-cond", Name: "Untargeted allow", Effect: authz.EffectAllow, Conditions: []abac.Condition{goodCond}},
			wantErr: false,
		},
		{
			name:    "action-scoped allow passes",
			rule:    abac.Rule{ID: "scoped", Name: "Scoped allow", Effect: authz.EffectAllow, Action: []string{"audit:read"}},
			wantErr: false,
		},
		{
			name:    "deny with empty Action passes (deny-all)",
			rule:    abac.Rule{ID: "deny-all", Name: "Deny everything", Effect: authz.EffectDeny},
			wantErr: false,
		},
		// ── anti-vacuity: structural-integrity violations still fail-closed ──
		{
			name:    "empty ID still rejected",
			rule:    abac.Rule{ID: "", Name: "x", Effect: authz.EffectAllow},
			wantErr: true,
		},
		{
			name:    "reserved _ prefix ID still rejected (attribution spoof)",
			rule:    abac.Rule{ID: "_default-deny", Name: "x", Effect: authz.EffectAllow},
			wantErr: true,
		},
		{
			name:    "empty Name still rejected",
			rule:    abac.Rule{ID: "r", Name: "", Effect: authz.EffectAllow},
			wantErr: true,
		},
		{
			name:    "invalid Effect still rejected",
			rule:    abac.Rule{ID: "r", Name: "x", Effect: authz.Effect(99)},
			wantErr: true,
		},
		{
			name: "invalid condition still propagates",
			rule: abac.Rule{
				ID: "r", Name: "x", Effect: authz.EffectAllow,
				Conditions: []abac.Condition{{Source: 0, Key: "dept", Operator: abac.OpEquals, Values: []string{"eng"}}},
			},
			wantErr: true,
		},
		{
			name:    "invalid Obligations still propagate",
			rule:    abac.Rule{ID: "r", Name: "x", Effect: authz.EffectAllow, Obligations: authz.Obligations{RowScope: tenant.RowScope(99)}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.rule.ValidateStored()
			if (err != nil) != tc.wantErr {
				t.Errorf("Rule.ValidateStored() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
