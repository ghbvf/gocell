package authorizationdecide

// evaluator_test.go — white-box unit tests for the action-aware evaluate loop
// (PR-10a #1348). These tests call evaluate() directly so they can exercise the
// action-match gate without needing the full Authorize() stack.

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// testAuditRead is the canonical action string for PermAuditRead, used throughout
// evaluator_test.go to avoid repeated string literals (F13 fix; mirrors the
// auditReadAction pattern in baseline_test.go — pkg/authz is already imported).
var testAuditRead = authz.PermAuditRead().String()

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

	// "tenant:read" / "other:write" are deliberately NON-baseline synthetic
	// actions: this test isolates tenant-policy action-targeting, so it must use
	// actions the built-in baseline does not allow (else baseline allow would mask
	// the tenant-rule behavior under test). Do NOT use real permissions like
	// config:read here — those are baseline-allowed for admin (PR-10b).

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
			action:    "other:write",
			wantAllow: false,
		},
		{
			name: "empty Action rule applies to every action (match-all preserved)",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("r1", authz.Obligations{}, roleCond),
			)},
			action:    "other:write",
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
				permitRuleWithAction("r1", []string{"tenant:read", testAuditRead}, roleCond),
			)},
			action:    "tenant:read",
			wantAllow: true,
		},
		{
			name: "multi-action target: matches second of the set",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{"tenant:read", testAuditRead}, roleCond),
			)},
			action:    testAuditRead,
			wantAllow: true,
		},
		{
			name: "multi-action target: action not in set — denies",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRuleWithAction("r1", []string{"tenant:read", testAuditRead}, roleCond),
			)},
			action:    "other:write",
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, _ := svc.evaluate(tt.policies, resolver, tt.action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// TestEvaluate_MatchedRuleID asserts evaluate surfaces the matched rule id so the
// PDP can log which rule decided (F12 #2027: distinguish "self via ownership rule"
// from "admin via baseline"). The first matching permit wins on Allow; the firing
// deny rule's id wins on forbid-wins; default-deny returns the "_default-deny"
// sentinel (the "_" prefix never collides with a real rule id).
func TestEvaluate_MatchedRuleID(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	const (
		ownerID   = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		synthetic = "thing:read"
	)
	ownerResolver := attributeResolver{
		principal:  &auth.Principal{Kind: auth.PrincipalUser, Subject: ownerID, TenantID: testTenantIDStr},
		resourceID: ownerID,
	}
	adminResolver := actionResolver(&auth.Principal{
		Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr, Roles: []string{auth.RoleAdmin},
	})
	adminOwnerResolver := attributeResolver{
		principal:  &auth.Principal{Kind: auth.PrincipalUser, Subject: ownerID, TenantID: testTenantIDStr, Roles: []string{auth.RoleAdmin}},
		resourceID: ownerID,
	}
	plainResolver := actionResolver(&auth.Principal{Kind: auth.PrincipalUser, Subject: "u", TenantID: testTenantIDStr})

	tests := []struct {
		name       string
		policies   []*abac.Policy
		resolver   attributeResolver
		action     string
		wantAllow  bool
		wantRuleID string
	}{
		{
			name:       "baseline ownership allow → self rule id",
			resolver:   ownerResolver,
			action:     authz.PermUserRead().String(),
			wantAllow:  true,
			wantRuleID: "baseline-user-read-self",
		},
		{
			name:       "baseline admin allow → admin rule id",
			resolver:   adminResolver,
			action:     testAuditRead,
			wantAllow:  true,
			wantRuleID: "baseline-audit-read-admin",
		},
		{
			name:       "multiple matching permits → first (admin, ordered before self) wins",
			resolver:   adminOwnerResolver,
			action:     authz.PermUserRead().String(),
			wantAllow:  true,
			wantRuleID: "baseline-user-read-admin",
		},
		{
			name: "tenant permit rule → tenant rule id",
			policies: []*abac.Policy{policyWith("p1",
				permitRuleWithAction("tenant-allow", []string{synthetic}))},
			resolver:   plainResolver,
			action:     synthetic,
			wantAllow:  true,
			wantRuleID: "tenant-allow",
		},
		{
			name: "forbid-wins → firing deny rule id",
			policies: []*abac.Policy{policyWith("p1",
				permitRuleWithAction("tenant-allow", []string{synthetic}),
				forbidRuleWithAction("tenant-deny", []string{synthetic}))},
			resolver:   plainResolver,
			action:     synthetic,
			wantAllow:  false,
			wantRuleID: "tenant-deny",
		},
		{
			name:       "default-deny → sentinel",
			resolver:   plainResolver,
			action:     synthetic,
			wantAllow:  false,
			wantRuleID: "_default-deny",
		},
		{
			// A matching permit whose merged obligation is invalid (FieldMask key with
			// whitespace → authz.Allow returns err) → fail-closed Deny + sentinel.
			name: "invalid combined obligations → sentinel",
			policies: []*abac.Policy{policyWith("p1",
				permitRule("bad-obl", authz.Obligations{FieldMask: authz.FieldMask{Fields: []string{"bad key"}}}))},
			resolver:   plainResolver,
			action:     synthetic,
			wantAllow:  false,
			wantRuleID: "_invalid-obligations",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, gotID := svc.evaluate(tt.policies, tt.resolver, tt.action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow(), "allow mismatch")
			assert.Equal(t, tt.wantRuleID, gotID, "matched rule id mismatch")
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

	dec, _ := svc.evaluate([]*abac.Policy{tenantForbid}, resolver, testAuditRead)
	assert.False(t, dec.IsAllow(), "tenant deny must override baseline allow (forbid-wins)")
}

// TestEvaluate_CrossAttrOwnership exercises the OpEqualsAttr cross-attribute
// operator (#1977): a rule that permits when subject.sub == resource.owner. The
// RHS attribute is resolved from the resource side, NOT from static Values. A
// missing RHS attribute is fail-closed (deny), mirroring the LHS not-found guard.
func TestEvaluate_CrossAttrOwnership(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	// Synthetic (non-baseline) action so only the tenant cross-attr rule can grant.
	const action = "thing:read"
	ownerCond := abac.Condition{
		Source:    abac.SourceSubject,
		Key:       "sub",
		Operator:  abac.OpEqualsAttr,
		RHSSource: abac.SourceResource,
		RHSKey:    "owner",
	}
	policy := policyWith("p1", permitRuleWithAction("owner-rule", []string{action}, ownerCond))

	mkResolver := func(subject string, owner []string) attributeResolver {
		p := &auth.Principal{Kind: auth.PrincipalUser, Subject: subject, TenantID: testTenantIDStr}
		attrs := map[string][]string{}
		if owner != nil {
			attrs["owner"] = owner
		}
		return attributeResolver{principal: p, resourceAttrs: attrs}
	}

	tests := []struct {
		name      string
		resolver  attributeResolver
		wantAllow bool
	}{
		{"subject equals owner — allow", mkResolver("u1", []string{"u1"}), true},
		{"subject not owner — deny", mkResolver("u1", []string{"u2"}), false},
		{"owner attr absent (RHS not-found) — deny", mkResolver("u1", nil), false},
		{"subject absent (LHS not-found) — deny", mkResolver("", []string{"u1"}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, _ := svc.evaluate([]*abac.Policy{policy}, tt.resolver, action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}
