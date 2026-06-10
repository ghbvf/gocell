package abac_test

import (
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/tenant"
)

const (
	validTenantID1 = tenant.TenantID("11111111-1111-1111-1111-111111111111")
	validTenantID2 = tenant.TenantID("22222222-2222-2222-2222-222222222222")
)

func makeValidRule(id string) abac.Rule {
	return abac.Rule{
		ID:     id,
		Name:   "rule " + id,
		Effect: authz.EffectAllow,
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
