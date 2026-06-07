package authorizationdecide

import (
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
)

// evaluate applies the default-deny + forbid-wins combining algorithm over every
// rule of every tenant policy, and returns the sealed authz.Decision.
//
// This is the SOLE construction site of authz.Allow / authz.Deny in the whole
// repository (caller-allowlist AUTHZ-DECISION-ALLOW-DENY-CALLER-01, the Hard
// downstream half of the sealed-Decision funnel begun in PR-6 #1344). Every
// Allow/Deny here is a genuine policy verdict; infrastructure failures are
// handled in Authorize, which returns a zero Decision (fail-closed) without
// touching Allow/Deny.
func (s *Service) evaluate(policies []*abac.Policy, r attributeResolver) authz.Decision {
	var permitObligations []authz.Obligations
	for _, p := range policies {
		for i := range p.Rules {
			rule := p.Rules[i]
			if !matchAllConditions(rule.Conditions, r) {
				continue
			}
			switch rule.Effect {
			case authz.EffectDeny:
				// forbid-wins: a single matching deny is final, regardless of
				// any matching permits (XACML §7.16 / Cedar).
				return authz.Deny("authorization-decide: denied by policy (forbid-wins)")
			case authz.EffectAllow:
				permitObligations = append(permitObligations, rule.Obligations)
			}
		}
	}
	if len(permitObligations) == 0 {
		// default-deny: no rule granted access.
		return authz.Deny("authorization-decide: no applicable permit (default-deny)")
	}
	dec, err := authz.Allow(mergeObligations(permitObligations))
	if err != nil {
		// An invalid combined obligation is a programmer/config error; deny
		// rather than emit an Allow that the PEP cannot enforce (fail-closed).
		s.logger.Error("authorization-decide: invalid combined obligations, denying", slog.Any("error", err))
		return authz.Deny("authorization-decide: invalid obligations")
	}
	return dec
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
// widens visibility) and RowScope is the NARROWEST (smallest) non-zero scope
// (combining row-visibility constraints conservatively never widens access).
// Both directions are fail-safe so that combining multiple permits can only
// tighten, never loosen, what the PEP enforces.
func mergeObligations(obligations []authz.Obligations) authz.Obligations {
	var merged authz.Obligations
	seen := make(map[string]struct{})
	var fields []string
	for _, o := range obligations {
		if o.RowScope != 0 && (merged.RowScope == 0 || o.RowScope < merged.RowScope) {
			merged.RowScope = o.RowScope
		}
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
