package abac

import (
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// reservedRuleIDPrefix is reserved for framework decision-attribution sentinels
// (authorizationdecide's `_default-deny` / `_invalid-obligations`, #2027 F12). A
// tenant- or policy-authored rule ID must never start with it, otherwise a tenant
// could mint a rule whose ID collides with a framework sentinel and make the PDP's
// matched_rule_id attribution ambiguous/spoofable (PR #2077 F1). No builtin
// baseline rule uses this prefix (they are all `baseline-*`), so the reservation
// is invisible to the platform's own rules and only constrains tenant input.
const reservedRuleIDPrefix = "_"

// Rule is the fundamental evaluation unit within a Policy. A Rule contains:
//   - An Effect (allow or deny) that determines the authorization outcome.
//   - Zero or more Conditions that are AND-combined. A Rule with no Conditions
//     matches every request (unconditional allow/deny).
//   - Optional Obligations that the PEP must discharge when the Rule fires.
//     Obligations are only meaningful when Effect == EffectAllow; PEPs MUST
//     NOT enforce obligations from a Rule with Effect == EffectDeny.
//
// ref: XACML 3.0 §5.9 — Rule element.
// ref: AWS Cedar rule model — permit/forbid + when/unless conditions.
type Rule struct {
	// ID uniquely identifies the rule within its parent Policy. Must be non-empty.
	ID string
	// Name is a human-readable label. Must be non-empty.
	Name string
	// Effect is the authorization verdict when all Conditions match.
	Effect authz.Effect
	// Conditions are AND-combined predicates. Zero conditions = unconditional match.
	Conditions []Condition
	// Action, when non-empty, restricts this rule to requests whose action is in
	// the set (e.g. {"audit:read"}). EMPTY = untargeted: the rule applies to every
	// action (preserving the pre-PR-10 evaluate-all semantics of existing
	// tenant-authored policies). This is the first-class action target that
	// condition.go deliberately deferred under YAGNI — PR-10 is the "real need".
	Action []string
	// Obligations are mandatory PEP actions on an Allow decision.
	// See Obligations.Validate() for the zero-value semantics.
	Obligations authz.Obligations
}

// Validate returns an error if the Rule is structurally invalid:
//   - ID must be non-empty.
//   - Name must be non-empty.
//   - Effect must be a valid authz.Effect.
//   - Each Condition (if any) must pass Condition.Validate().
//   - Obligations must pass Obligations.Validate().
func (r Rule) Validate() error {
	if r.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: rule ID must not be empty")
	}
	if strings.HasPrefix(r.ID, reservedRuleIDPrefix) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"abac: rule ID must not start with '_' (reserved for framework decision-attribution sentinels)")
	}
	if r.Name == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: rule Name must not be empty")
	}
	if err := r.Effect.Validate(); err != nil {
		return err
	}
	for _, cond := range r.Conditions {
		if err := cond.Validate(); err != nil {
			return err
		}
	}
	return r.Obligations.Validate()
}
