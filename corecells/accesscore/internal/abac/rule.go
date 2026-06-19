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
	// Action restricts this rule to requests whose action is in the set (e.g.
	// {"audit:read"}). For an Allow rule it MUST be non-empty (#1979): an allow
	// must name which route gate(s) it widens, otherwise one blank allow (empty
	// Action + empty Conditions) would unconditionally permit every action and
	// blow open the route gate for all permissions. Empty Action is valid ONLY for
	// a Deny rule, where it means deny-all (forbid-wins over every action). The
	// non-empty-for-Allow invariant is enforced by Validate (write side → 422) and,
	// in depth, by the evaluator's read-side fail-closed (an empty-Action Allow
	// that slips past validation is treated as not-applicable and never grants).
	Action []string
	// Obligations are mandatory PEP actions on an Allow decision.
	// See Obligations.Validate() for the zero-value semantics.
	Obligations authz.Obligations
}

// Validate returns an error if the Rule is structurally invalid:
//   - ID must be non-empty.
//   - Name must be non-empty.
//   - Effect must be a valid authz.Effect.
//   - An Allow rule must declare at least one Action (#1979); empty Action is
//     valid only for a Deny rule (deny-all).
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
	// #1979: an Allow rule must name at least one Action. An empty-Action Allow
	// combined with empty Conditions unconditionally permits every action — a
	// single such tenant rule would blow open the route gate for all permissions.
	// Deny is exempt: an untargeted deny is a legitimate deny-all (forbid-wins).
	if r.Effect == authz.EffectAllow && len(r.Action) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"abac: allow rule must declare at least one Action")
	}
	for _, cond := range r.Conditions {
		if err := cond.Validate(); err != nil {
			return err
		}
	}
	return r.Obligations.Validate()
}
