package authorizationdecide

import (
	"log/slog"
	"slices"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// Sentinel matched-rule ids for the two evaluate outcomes that are not the id of
// any single rule. The "_" prefix never collides with a real abac.Rule.ID (rule
// ids are non-empty, dotted/dashed names like "baseline-user-read-self" and never
// begin with "_"), so the PDP decision log can carry an unambiguous rule attribution
// even when no concrete rule fired (#2027 F12).
const (
	ruleIDDefaultDeny        = "_default-deny"
	ruleIDInvalidObligations = "_invalid-obligations"
)

// evaluate applies the default-deny + forbid-wins combining algorithm over every
// rule of every tenant policy (plus the built-in baseline rules) and returns the
// sealed authz.Decision plus the id of the rule that decided it (#2027 F12): the
// firing deny rule's id on forbid-wins, the FIRST matching permit rule's id on
// Allow (baseline rules are evaluated first, so the order is deterministic), or a
// sentinel for default-deny / invalid-obligations. The id is for observability
// only — it does NOT affect the verdict.
//
// A rule is considered only when its action gate passes: len(rule.Action)==0 (the
// rule is untargeted and applies to every action, preserving pre-PR-10 semantics)
// OR slices.Contains(rule.Action, action). The existing matchAllConditions gate
// must also hold. Both checks must pass for a rule to fire.
//
// This is the SOLE construction site of authz.Allow / authz.Deny in the whole
// repository (caller-allowlist AUTHZ-DECISION-ALLOW-DENY-CALLER-01, the Hard
// downstream half of the sealed-Decision funnel begun in PR-6 #1344). Every
// Allow/Deny here is a genuine policy verdict; infrastructure failures are
// handled in Authorize, which returns a zero Decision (fail-closed) without
// touching Allow/Deny.
func (s *Service) evaluate(policies []*abac.Policy, r attributeResolver, action string) (authz.Decision, string) {
	var permitObligations []authz.Obligations
	var firstPermitID string

	// Baseline rules are evaluated first; tenant policies follow.
	// Forbid-wins is global: a deny anywhere is final.
	for _, rule := range builtinBaselineRules() {
		if done, dec := applyRule(rule, r, action, &permitObligations, &firstPermitID); done {
			return dec, rule.ID
		}
	}
	for _, p := range policies {
		for i := range p.Rules {
			if done, dec := applyRule(p.Rules[i], r, action, &permitObligations, &firstPermitID); done {
				return dec, p.Rules[i].ID
			}
		}
	}

	if len(permitObligations) == 0 {
		// default-deny: no rule granted access.
		return authz.Deny("authorization-decide: no applicable permit (default-deny)"), ruleIDDefaultDeny
	}
	dec, err := authz.Allow(mergeObligations(permitObligations))
	if err != nil {
		// An invalid combined obligation is a programmer/config error; deny
		// rather than emit an Allow that the PEP cannot enforce (fail-closed).
		s.logger.Error("authorization-decide: invalid combined obligations, denying",
			slog.String("action", action),
			slog.Any("error", redaction.RedactError(err)))
		return authz.Deny("authorization-decide: invalid obligations"), ruleIDInvalidObligations
	}
	return dec, firstPermitID
}

// applyRule tests the action gate and condition gate for a single rule, then
// handles the effect. It appends to *permits on Allow (recording the first
// matching permit's id into *firstPermitID for decision attribution), and returns
// (true, Deny) on the first matching Deny (forbid-wins early exit). It returns
// (false, {}) when the rule does not fire (gate miss or unknown effect).
func applyRule(
	rule abac.Rule, r attributeResolver, action string, permits *[]authz.Obligations, firstPermitID *string,
) (done bool, dec authz.Decision) {
	if len(rule.Action) > 0 && !slices.Contains(rule.Action, action) {
		return false, authz.Decision{}
	}
	if !matchAllConditions(rule.Conditions, r) {
		return false, authz.Decision{}
	}
	switch rule.Effect {
	case authz.EffectDeny:
		// forbid-wins: a single matching deny is final (XACML §7.16 / Cedar).
		return true, authz.Deny("authorization-decide: denied by policy (forbid-wins)")
	case authz.EffectAllow:
		*permits = append(*permits, rule.Obligations)
		if *firstPermitID == "" {
			*firstPermitID = rule.ID
		}
	}
	return false, authz.Decision{}
}

// matchAllConditions reports whether every condition matches (AND-combined). A
// rule with zero conditions matches unconditionally.
func matchAllConditions(conditions []abac.Condition, r attributeResolver) bool {
	for _, c := range conditions {
		if !matchCondition(c, r) {
			return false
		}
	}
	return true
}

// matchCondition evaluates a single condition. A condition whose attribute is
// not found is unsatisfied (fail-closed); an unknown operator is unsatisfied.
//
// Note the negative operators (OpNotEquals / OpNotIn) are also fail-closed on a
// missing attribute: found=false returns false (condition unsatisfied), NOT
// "vacuously true". So a "blacklist exclude" permit rule (e.g. region not_in
// {us-gov}) requires the attribute to be present — a subject missing the
// attribute does not satisfy the condition and therefore is not granted by that
// rule. This is intentional: a missing attribute must never widen access. Policy
// authors relying on negative conditions must ensure the attribute is supplied.
//
// OpEqualsAttr compares the LHS attribute against a SECOND resolved attribute
// (RHSSource, RHSKey) instead of static Values (subject.sub == resource.id). The
// RHS resolve is fail-closed on the same terms as the LHS: a not-found RHS makes
// the condition unsatisfied (never vacuously true), so a missing owner/id
// attribute cannot grant ownership. Both resolve `found` results must be honored
// — statically guarded by AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01.
func matchCondition(c abac.Condition, r attributeResolver) bool {
	vals, found := r.resolve(c.Source, c.Key)
	if !found {
		return false
	}
	switch c.Operator {
	case abac.OpEquals, abac.OpIn:
		return anyIn(vals, c.Values)
	case abac.OpNotEquals, abac.OpNotIn:
		return !anyIn(vals, c.Values)
	case abac.OpEqualsAttr:
		rvals, rfound := r.resolve(c.RHSSource, c.RHSKey)
		if !rfound {
			return false
		}
		return anyIn(vals, rvals)
	default:
		return false
	}
}

// anyIn reports whether any value in vals is present in set.
func anyIn(vals, set []string) bool {
	for _, v := range vals {
		for _, s := range set {
			if v == s {
				return true
			}
		}
	}
	return false
}

// mergeObligations fail-safely combines the obligations of all matching permit
// rules: FieldMask is the UNION of masked fields (masking a superset never
// widens visibility) and RowScope is the NARROWEST scope (via tenant.RowScope's
// Narrower, which owns the strictness ordering — no raw numeric comparison here).
// Both directions are fail-safe so that combining multiple permits can only
// tighten, never loosen, what the PEP enforces.
func mergeObligations(obligations []authz.Obligations) authz.Obligations {
	var merged authz.Obligations
	seen := make(map[string]struct{})
	var fields []string
	for _, o := range obligations {
		merged.RowScope = merged.RowScope.Narrower(o.RowScope)
		for _, f := range o.FieldMask.Fields {
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			fields = append(fields, f)
		}
	}
	merged.FieldMask = authz.FieldMask{Fields: fields}
	return merged
}
