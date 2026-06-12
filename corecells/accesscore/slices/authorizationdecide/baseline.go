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
		ID:     "baseline-audit-read-admin",
		Name:   "Baseline: allow admin/super-admin to read audit ledger",
		Effect: authz.EffectAllow,
		Action: []string{authz.PermAuditRead().String()},
		Conditions: []abac.Condition{
			{
				Source:   abac.SourceSubject,
				Key:      "roles",
				Operator: abac.OpIn,
				Values:   []string{runtimeauth.RoleAdmin, runtimeauth.RoleSuperAdmin},
			},
		},
	},
	{
		ID:     "baseline-system-read-admin",
		Name:   "Baseline: allow admin/super-admin to read runtime/system health (#1860)",
		Effect: authz.EffectAllow,
		Action: []string{authz.PermSystemRead().String()},
		Conditions: []abac.Condition{
			{
				Source:   abac.SourceSubject,
				Key:      "roles",
				Operator: abac.OpIn,
				Values:   []string{runtimeauth.RoleAdmin, runtimeauth.RoleSuperAdmin},
			},
		},
	},
}

// builtinBaselineRules returns the tenant-agnostic built-in baseline rule set.
//
// Built-in baseline reproducing the existing role→endpoint gate. action-scoped +
// role-conditioned — NOT a downgrade allow-all (FR-011 fail-closed governs
// missing-attr/store-err; the baseline governs the default policy set; the two do
// not conflict). PR-10b extends one rule per migrated endpoint.
//
// PR-10a baseline: exactly one rule — PermAuditRead() is allowed for admin and
// super-admin principals. The condition uses Source=subject, Key="roles",
// Operator=OpIn so it matches any principal whose Roles slice contains at least
// one of the listed role values. Role constants are imported from runtime/auth so
// the values match exactly what attributeResolver.resolveSubject emits for the
// "roles" key (r.principal.Roles).
//
// Returns the package-level builtinBaseline slice directly (no allocation).
func builtinBaselineRules() []abac.Rule {
	return builtinBaseline
}
