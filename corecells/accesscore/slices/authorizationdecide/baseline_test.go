package authorizationdecide

// baseline_test.go — unit tests for the built-in baseline rule set (PR-10a #1348).
// Tests prove: admin+audit:read → Allow; super-admin+audit:read → Allow;
// ordinary user+audit:read → Deny; admin+different action → NOT allowed by baseline.

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

func TestBuiltinBaseline_AuditRead(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	auditReadAction := authz.PermAuditRead().String()

	tests := []struct {
		name      string
		principal *auth.Principal
		action    string
		wantAllow bool
	}{
		{
			name: "admin + audit:read → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "admin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleAdmin},
			},
			action:    auditReadAction,
			wantAllow: true,
		},
		{
			name: "super-admin + audit:read → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "sadmin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleSuperAdmin},
			},
			action:    auditReadAction,
			wantAllow: true,
		},
		{
			name: "ordinary user + audit:read → Deny (default-deny, no matching baseline rule)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-1", TenantID: testTenantIDStr,
				Roles: []string{"viewer"},
			},
			action:    auditReadAction,
			wantAllow: false,
		},
		{
			name: "no roles + audit:read → Deny (default-deny)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-2", TenantID: testTenantIDStr,
				Roles: nil,
			},
			action:    auditReadAction,
			wantAllow: false,
		},
		{
			name: "admin + other:write → NOT allowed by baseline",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "admin-3", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleAdmin},
			},
			action:    "other:write",
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Evaluate with NO tenant policies — only baseline applies.
			resolver := attributeResolver{principal: tt.principal}
			dec, _ := svc.evaluate(nil, resolver, tt.action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// TestBuiltinBaseline_SystemRead proves the #1860 baseline rule: admin and
// super-admin are granted system:read (the aggregated cell-health gate), while
// ordinary users / role-less principals are denied — action-scoped, role-literal-free.
func TestBuiltinBaseline_SystemRead(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	systemReadAction := authz.PermSystemRead().String()

	tests := []struct {
		name      string
		principal *auth.Principal
		wantAllow bool
	}{
		{
			name: "admin + system:read → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "admin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleAdmin},
			},
			wantAllow: true,
		},
		{
			name: "super-admin + system:read → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "sadmin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleSuperAdmin},
			},
			wantAllow: true,
		},
		{
			name: "ordinary user + system:read → Deny (default-deny)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-1", TenantID: testTenantIDStr,
				Roles: []string{"viewer"},
			},
			wantAllow: false,
		},
		{
			name: "no roles + system:read → Deny (default-deny)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-2", TenantID: testTenantIDStr,
				Roles: nil,
			},
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := attributeResolver{principal: tt.principal}
			dec, _ := svc.evaluate(nil, resolver, systemReadAction)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// TestBuiltinBaseline_SessionVerify proves the #1154 baseline rule: admin and
// super-admin are granted session:verify (the accesscore sessionverifyrpc gRPC
// gate), while ordinary users / role-less principals are denied — action-scoped,
// role-literal-free, the same shape as the system:read rule. The grant surface is
// what the gRPC PDP gate (#2008) enforces for grpc.auth.session.verify.v1.
func TestBuiltinBaseline_SessionVerify(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	sessionVerifyAction := authz.PermSessionVerify().String()

	tests := []struct {
		name      string
		principal *auth.Principal
		wantAllow bool
	}{
		{
			name: "admin + session:verify → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "admin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleAdmin},
			},
			wantAllow: true,
		},
		{
			name: "super-admin + session:verify → Allow (baseline grants)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "sadmin-1", TenantID: testTenantIDStr,
				Roles: []string{auth.RoleSuperAdmin},
			},
			wantAllow: true,
		},
		{
			name: "ordinary user + session:verify → Deny (default-deny)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-1", TenantID: testTenantIDStr,
				Roles: []string{"viewer"},
			},
			wantAllow: false,
		},
		{
			name: "no roles + session:verify → Deny (default-deny)",
			principal: &auth.Principal{
				Kind: auth.PrincipalUser, Subject: "user-2", TenantID: testTenantIDStr,
				Roles: nil,
			},
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := attributeResolver{principal: tt.principal}
			dec, _ := svc.evaluate(nil, resolver, sessionVerifyAction)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// TestBuiltinBaseline_ConfigcorePerms proves the PR-10b configcore baseline
// rules reproduce the existing admin gate: admin/super-admin → Allow, ordinary
// user / no-roles → Deny, for each of the 5 migrated configcore permissions.
// One baseline rule per migrated endpoint, mirroring the auditquery rule.
func TestBuiltinBaseline_ConfigcorePerms(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	perms := []string{
		authz.PermConfigRead().String(),
		authz.PermConfigWrite().String(),
		authz.PermConfigPublish().String(),
		authz.PermFlagRead().String(),
		authz.PermFlagWrite().String(),
	}

	roleCases := []struct {
		name      string
		roles     []string
		wantAllow bool
	}{
		{"admin → Allow", []string{auth.RoleAdmin}, true},
		{"super-admin → Allow", []string{auth.RoleSuperAdmin}, true},
		{"ordinary user → Deny", []string{"viewer"}, false},
		{"no roles → Deny", nil, false},
	}

	for _, action := range perms {
		for _, rc := range roleCases {
			t.Run(action+" / "+rc.name, func(t *testing.T) {
				resolver := attributeResolver{principal: &auth.Principal{
					Kind: auth.PrincipalUser, Subject: "subj-1", TenantID: testTenantIDStr,
					Roles: rc.roles,
				}}
				dec, _ := svc.evaluate(nil, resolver, action)
				assert.Equal(t, rc.wantAllow, dec.IsAllow())
			})
		}
	}
}

// TestBuiltinBaseline_AccesscorePerms proves the PR-10c accesscore baseline rules
// reproduce the existing admin gate: admin/super-admin → Allow, ordinary user /
// no-roles → Deny, for each of the 5 migrated accesscore permissions. The self
// branch of the SelfOr-migrated permissions (user:read/write, role:read) is now a
// baseline ownership rule (subject.sub == resource.id via RequirePermissionForResource,
// #1977 Batch B), not a request-shape exemption in auth.RequirePermissionOrSelf.
// This test pins the admin (admin/super-admin) baseline path; the ownership rules
// are pinned by TestBuiltinBaseline_SelfOwnership below.
func TestBuiltinBaseline_AccesscorePerms(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	perms := []string{
		authz.PermPolicyRead().String(),
		authz.PermPolicyWrite().String(),
		authz.PermUserRead().String(),
		authz.PermUserWrite().String(),
		authz.PermRoleRead().String(),
	}

	roleCases := []struct {
		name      string
		roles     []string
		wantAllow bool
	}{
		{"admin → Allow", []string{auth.RoleAdmin}, true},
		{"super-admin → Allow", []string{auth.RoleSuperAdmin}, true},
		{"ordinary user → Deny", []string{"viewer"}, false},
		{"no roles → Deny", nil, false},
	}

	for _, action := range perms {
		for _, rc := range roleCases {
			t.Run(action+" / "+rc.name, func(t *testing.T) {
				resolver := attributeResolver{principal: &auth.Principal{
					Kind: auth.PrincipalUser, Subject: "subj-1", TenantID: testTenantIDStr,
					Roles: rc.roles,
				}}
				dec, _ := svc.evaluate(nil, resolver, action)
				assert.Equal(t, rc.wantAllow, dec.IsAllow())
			})
		}
	}
}

// TestBuiltinBaseline_RulesAreNonEmpty guards anti-vacuity: the baseline must
// contain at least one rule so an empty return from builtinBaselineRules()
// cannot silently skip all enforcement.
func TestBuiltinBaseline_RulesAreNonEmpty(t *testing.T) {
	rules := builtinBaselineRules()
	assert.NotEmpty(t, rules, "builtinBaselineRules must return at least one rule")
}

// TestBuiltinBaseline_AllRulesValid guards that every baseline rule passes
// abac.Rule.Validate(), so the baseline cannot introduce an invalid rule that
// would propagate through the evaluation loop silently.
func TestBuiltinBaseline_AllRulesValid(t *testing.T) {
	for i, r := range builtinBaselineRules() {
		if err := r.Validate(); err != nil {
			t.Errorf("builtinBaselineRules()[%d] (ID=%q) failed Validate(): %v", i, r.ID, err)
		}
	}
}

// TestBuiltinBaseline_SelfOwnership pins the identity-ownership baseline rules
// added in #1977 Batch B: subject.sub == resource.id grants user:read, user:write,
// and role:read for the owning subject. Admin path is unchanged (tested above).
//
// resource.id is exposed via attributeResolver.resourceID (added in Batch B):
// a non-empty resourceID makes resource.id found=true; empty makes found=false.
func TestBuiltinBaseline_SelfOwnership(t *testing.T) {
	svc := &Service{logger: slog.Default()}

	const (
		ownerID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		nonOwnerID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)

	ownerPrincipal := &auth.Principal{
		Kind: auth.PrincipalUser, Subject: ownerID, TenantID: testTenantIDStr,
		Roles: []string{"user"},
	}
	nonOwnerPrincipal := &auth.Principal{
		Kind: auth.PrincipalUser, Subject: nonOwnerID, TenantID: testTenantIDStr,
		Roles: []string{"user"},
	}
	adminPrincipal := &auth.Principal{
		Kind: auth.PrincipalUser, Subject: nonOwnerID, TenantID: testTenantIDStr,
		Roles: []string{auth.RoleAdmin},
	}

	ownershipActions := []string{
		authz.PermUserRead().String(),
		authz.PermUserWrite().String(),
		authz.PermRoleRead().String(),
	}
	nonOwnershipAction := authz.PermConfigRead().String()

	tests := []struct {
		name       string
		principal  *auth.Principal
		resourceID string
		action     string
		wantAllow  bool
	}{
		// Self-ownership: subject.sub == resource.id → Allow for each ownership action.
		{
			name:      "owner + user:read + matching resource.id → Allow",
			principal: ownerPrincipal, resourceID: ownerID,
			action: authz.PermUserRead().String(), wantAllow: true,
		},
		{
			name:      "owner + user:write + matching resource.id → Allow",
			principal: ownerPrincipal, resourceID: ownerID,
			action: authz.PermUserWrite().String(), wantAllow: true,
		},
		{
			name:      "owner + role:read + matching resource.id → Allow",
			principal: ownerPrincipal, resourceID: ownerID,
			action: authz.PermRoleRead().String(), wantAllow: true,
		},
		// Non-owner: subject.sub != resource.id → no ownership rule fires → Deny.
		{
			name:      "non-owner + user:read → Deny (no ownership, no admin)",
			principal: nonOwnerPrincipal, resourceID: ownerID,
			action: authz.PermUserRead().String(), wantAllow: false,
		},
		{
			name:      "non-owner + user:write → Deny",
			principal: nonOwnerPrincipal, resourceID: ownerID,
			action: authz.PermUserWrite().String(), wantAllow: false,
		},
		{
			name:      "non-owner + role:read → Deny",
			principal: nonOwnerPrincipal, resourceID: ownerID,
			action: authz.PermRoleRead().String(), wantAllow: false,
		},
		// Admin bypass: admin role grants regardless of resource.id match.
		{
			name:      "admin + user:read + different resource.id → Allow (admin baseline)",
			principal: adminPrincipal, resourceID: ownerID,
			action: authz.PermUserRead().String(), wantAllow: true,
		},
		{
			name:      "admin + role:read + different resource.id → Allow",
			principal: adminPrincipal, resourceID: ownerID,
			action: authz.PermRoleRead().String(), wantAllow: true,
		},
		// Empty resourceID: resource.id not-found → ownership rule can't fire → Deny.
		{
			name:      "owner subject + user:read + empty resourceID → Deny (resource.id not-found)",
			principal: ownerPrincipal, resourceID: "",
			action: authz.PermUserRead().String(), wantAllow: false,
		},
		// Ownership rules are action-scoped: config:read is NOT in the ownership set.
		{
			name:      "owner subject + config:read + matching resource.id → Deny (not an ownership action)",
			principal: ownerPrincipal, resourceID: ownerID,
			action: nonOwnershipAction, wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := attributeResolver{principal: tt.principal, resourceID: tt.resourceID}
			dec, _ := svc.evaluate(nil, resolver, tt.action)
			assert.Equal(t, tt.wantAllow, dec.IsAllow(), "action=%q resourceID=%q roles=%v",
				tt.action, tt.resourceID, tt.principal.Roles)
		})
	}

	// Action-scope guard: ownership rules must NOT fire for any non-ownership action.
	for _, action := range []string{
		authz.PermConfigRead().String(),
		authz.PermConfigWrite().String(),
		authz.PermAuditRead().String(),
		authz.PermPolicyRead().String(),
	} {
		t.Run("ownership rule not fired for "+action, func(t *testing.T) {
			resolver := attributeResolver{principal: ownerPrincipal, resourceID: ownerID}
			dec, _ := svc.evaluate(nil, resolver, action)
			assert.False(t, dec.IsAllow(),
				"ownership rule must not grant non-ownership action %q to non-admin", action)
		})
	}

	// All 3 ownership actions must be covered.
	for _, action := range ownershipActions {
		t.Run("ownership rule fires for "+action, func(t *testing.T) {
			resolver := attributeResolver{principal: ownerPrincipal, resourceID: ownerID}
			dec, _ := svc.evaluate(nil, resolver, action)
			assert.True(t, dec.IsAllow(), "ownership rule must grant action %q to owner", action)
		})
	}
}
