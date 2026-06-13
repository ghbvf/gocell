package authorizationdecide

// baseline_test.go — unit tests for the built-in baseline rule set (PR-10a #1348).
// Tests prove: admin+audit:read → Allow; super-admin+audit:read → Allow;
// ordinary user+audit:read → Deny; admin+different action → NOT allowed by baseline.

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
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
			dec := svc.evaluate(nil, resolver, tt.action)
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
				dec := svc.evaluate(nil, resolver, action)
				assert.Equal(t, rc.wantAllow, dec.IsAllow())
			})
		}
	}
}

// TestBuiltinBaseline_AccesscorePerms proves the PR-10c accesscore baseline rules
// reproduce the existing admin gate: admin/super-admin → Allow, ordinary user /
// no-roles → Deny, for each of the 5 migrated accesscore permissions. The self
// branch of the SelfOr-migrated permissions (user:read/write, role:read) is NOT a
// baseline rule — it is a request-shape exemption in auth.RequirePermissionOrSelf,
// so the baseline only needs to reproduce the admin (now admin/super-admin) path.
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
				dec := svc.evaluate(nil, resolver, action)
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
