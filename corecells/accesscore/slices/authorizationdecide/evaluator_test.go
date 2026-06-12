package authorizationdecide

// evaluator_test.go — white-box unit tests for the action-aware evaluate loop
// (PR-10a #1348). These tests call evaluate() directly so they can exercise the
// action-match gate without needing the full Authorize() stack.

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// testAuditRead is the canonical action string for PermAuditRead, used throughout
// evaluator_test.go to avoid repeated string literals (F13 fix; mirrors the
// auditReadAction pattern in baseline_test.go — pkg/authz is already imported).
var testAuditRead = authz.PermAuditRead.String()

// actionResolver builds an attributeResolver with just the principal (no env/resource).
func actionResolver(p *auth.Principal) attributeResolver {
	return attributeResolver{principal: p}
}

// permitRuleWithAction builds a permit rule scoped to a specific set of actions.
func permitRuleWithAction(id string, actions []string, conds ...abac.Condition) abac.Rule {
	return abac.Rule{
		ID:         id,
		Name:       id,
		Effect:     authz.EffectAllow,
		Action:     actions,
		Conditions: conds,
	}
}

// forbidRuleWithAction builds a forbid rule scoped to a specific set of actions.
func forbidRuleWithAction(id string, actions []string) abac.Rule {
	return abac.Rule{
		ID:     id,
		Name:   id,
		Effect: authz.EffectDeny,
		Action: actions,
	}
}

func TestEvaluate_ActionTargeting(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	roleCond := cond(abac.SourceSubject, "role", abac.OpIn, auth.RoleAdmin)
	adminPrincipal := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr, Roles: []string{auth.RoleAdmin}}
	resolver := actionResolver(adminPrincipal)

	tests := []struct {
		name      string
		policies  []*abac.Policy
		action    string
		wantAllow bool
	}{
		{
			name: "action matches targeted rule — allows",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{testAuditRead}, roleCond),
			)},
			action:    testAuditRead,
			wantAllow: true,
		},
		{
			name: "action does not match targeted rule — default-deny",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{testAuditRead}, roleCond),
			)},
			action:    "config:read",
			wantAllow: false,
		},
		{
			name: "empty Action rule applies to every action (match-all preserved)",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("r1", authz.Obligations{}, roleCond),
			)},
			action:    "config:read",
			wantAllow: true,
		},
		{
			name: "empty Action rule applies to every action including audit:read",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("r1", authz.Obligations{}, roleCond),
			)},
			action:    testAuditRead,
			wantAllow: true,
		},
		{
			name: "forbid-wins: targeted deny overrides a matching permit for the same action",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("allow", []string{testAuditRead}, roleCond),
				forbidRuleWithAction("deny", []string{testAuditRead}),
			)},
			action:    testAuditRead,
			wantAllow: false,
		},
		{
			name: "targeted deny does NOT fire for a different action (no forbid-wins for mismatched action)",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("allow", authz.Obligations{}, roleCond),
				forbidRuleWithAction("deny", []string{"config:delete"}),
			)},
			action:    testAuditRead,
			wantAllow: true,
		},
		{
			name: "untargeted deny (empty Action) still forbid-wins over any action",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("allow", authz.Obligations{}, roleCond),
				forbidRule("deny"),
			)},
			action:    testAuditRead,
			wantAllow: false,
		},
		{
			name: "multi-action target: matches first of the set",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{"config:read", testAuditRead}, roleCond),
			)},
			action:    "config:read",
			wantAllow: true,
		},
		{
			name: "multi-action target: matches second of the set",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{"config:read", testAuditRead}, roleCond),
			)},
			action:    testAuditRead,
			wantAllow: true,
		},
		{
			name: "multi-action target: action not in set — denies",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{"config:read", testAuditRead}, roleCond),
			)},
			action:    "other:write",
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := svc.evaluate(tt.policies, resolver, tt.action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// TestEvaluate_BaselineDenyOverridesPermit guards a critical forbid-wins invariant:
// a matching EffectDeny rule in a tenant policy wins over the baseline allow.
func TestEvaluate_BaselineDenyOverridesPermit(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	adminPrincipal := &auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr, Roles: []string{auth.RoleAdmin}}
	resolver := actionResolver(adminPrincipal)

	// Tenant policy explicitly forbids audit:read for admin.
	tenantForbid := policyWith(
		"p1",
		forbidRuleWithAction("explicit-deny", []string{testAuditRead}),
	)

	dec := svc.evaluate([]*abac.Policy{tenantForbid}, resolver, testAuditRead)
	assert.False(t, dec.IsAllow(), "tenant deny must override baseline allow (forbid-wins)")
}
