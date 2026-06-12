package authorizationdecide

// baseline.go — built-in baseline rule set for the ABAC PDP (PR-10a #1348).

import (
	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	runtimeauth "github.com/ghbvf/gocell/runtime/auth"
)

// builtinBaseline is the package-level rule set returned by builtinBaselineRules.
// Defined once so callers that only need len() pay zero allocation cost and the
// evaluate loop and Authorize log read from the same backing array (F4 fix).
var builtinBaseline = []abac.Rule{
	{
		ID:         "baseline-audit-read-admin",
		Name:       "Baseline: allow admin/super-admin to read audit ledger",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermAuditRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-system-read-admin",
		Name:       "Baseline: allow admin/super-admin to read runtime/system health (#1860)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermSystemRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// configcore baseline (PR-10b #1348): one allow rule per migrated slice via
	// the shared adminOrSuperAdmin() condition. NOTE: the old configcore gates were
	// auth.AnyRole(RoleAdmin) — admin-only — so this WIDENS them to admit super-admin
	// (super-admin ⊇ admin; a deliberate relaxation, zero prod impact as superadmin
	// is not yet issued). See ADR §"Amendment: PR-10b" threat-model re-eval + ruling.
	// action-scoped + role-conditioned — same shape as the audit rule above.
	{
		ID:         "baseline-config-read-admin",
		Name:       "Baseline: allow admin/super-admin to read configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-config-write-admin",
		Name:       "Baseline: allow admin/super-admin to write configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-config-publish-admin",
		Name:       "Baseline: allow admin/super-admin to publish/rollback configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigPublish().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-flag-read-admin",
		Name:       "Baseline: allow admin/super-admin to read/evaluate feature flags",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermFlagRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-flag-write-admin",
		Name:       "Baseline: allow admin/super-admin to write feature flags",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermFlagWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
}

// adminOrSuperAdmin is the shared baseline condition: subject.roles ∈
// {admin, super-admin}. Extracted because all current baseline rules share this
// same admin+super-admin condition (≥3 identical literals → constant, per
// go-standards). For audit this matched the prior gate exactly; for configcore it
// widens the prior admin-only gate (see configcore baseline note above + the ADR).
// Role constants come from runtime/auth so the values match exactly what
// attributeResolver.resolveSubject emits for the "roles" key (r.principal.Roles).
func adminOrSuperAdmin() abac.Condition {
	return abac.Condition{
		Source:   abac.SourceSubject,
		Key:      "roles",
		Operator: abac.OpIn,
		Values:   []string{runtimeauth.RoleAdmin, runtimeauth.RoleSuperAdmin},
	}
}

// builtinBaselineRules returns the tenant-agnostic built-in baseline rule set.
//
// Built-in baseline reproducing the existing role→endpoint gate. action-scoped +
// role-conditioned — NOT a downgrade allow-all (FR-011 fail-closed governs
// missing-attr/store-err; the baseline governs the default policy set; the two do
// not conflict). One rule per migrated endpoint; PR-10b/PR-10c extend it.
//
// Current baseline: PermAuditRead() (PR-10a) + the 5 configcore permissions
// (PR-10b: config:read/write/publish, flag:read/write), each allowed for admin
// and super-admin principals via adminOrSuperAdmin(). Each rule is action-scoped
// (Action target) so a baseline allow for one permission never leaks to another.
//
// Returns the package-level builtinBaseline slice directly (no allocation).
func builtinBaselineRules() []abac.Rule {
	return builtinBaseline
}
