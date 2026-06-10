package abac

import (
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

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
