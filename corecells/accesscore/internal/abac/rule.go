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

// Validate is the AUTHORING validation profile: it enforces the stored-read
// structural-integrity checks (validateStructural) PLUS the write-side-only
// invariants a caller must satisfy when authoring or mutating a rule. Use it on
// every WRITE path (Create / Update, tenant policy authoring, in-memory + PG
// repos). The single authoring-only delta today is the #1979 non-empty-Action
// rule for an Allow effect.
//
//   - structural integrity (see validateStructural): ID non-empty + not reserved,
//     Name non-empty, valid Effect, valid Conditions, valid Obligations.
//   - authoring-only: an Allow rule must declare at least one Action (#1979);
//     empty Action is valid only for a Deny rule (deny-all).
//
// Read paths MUST use ValidateStored, not Validate: empty-Action Allow is an
// authoring mistake to reject on write (422), but a persisted such row is benign
// — the evaluator renders it inert (never grants), so re-rejecting it on read
// would 503 the whole tenant PDP for one legacy row (#2409 F1).
func (r Rule) Validate() error {
	if err := r.validateStructural(); err != nil {
		return err
	}
	// #1979 (authoring-only): an Allow rule must name at least one Action. An
	// empty-Action Allow combined with empty Conditions unconditionally permits
	// every action — a single such tenant rule would blow open the route gate for
	// all permissions. Deny is exempt: an untargeted deny is a legitimate deny-all
	// (forbid-wins). This is NOT a storage-integrity check, so it lives here in the
	// authoring profile and is deliberately absent from ValidateStored.
	if r.Effect == authz.EffectAllow && len(r.Action) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"abac: allow rule must declare at least one Action")
	}
	return nil
}

// ValidateStored is the STORED-READ validation profile: structural-integrity only,
// legacy-tolerant. It is the defensive re-validation a repository applies to a row
// reconstructed from durable storage (PG scanPolicy), guarding against corrupt or
// forward-incompatible rows WITHOUT re-applying authoring-only invariants. The set
// of checks is exactly validateStructural — so ValidateStored ⊆ Validate (a row
// that passes authoring also passes stored-read), and adding a genuine integrity
// check to validateStructural strengthens both profiles, while an authoring-only
// invariant added to Validate's delta never leaks onto the read path.
//
// Why empty-Action Allow is NOT rejected here: such a row is benign at read time —
// the evaluator's applyRule treats an empty-Action Allow as not-applicable (never
// grants), so it cannot widen access. Rejecting it would convert a single legacy
// persisted row into a tenant-wide PDP 503 (#2409 F1).
func (r Rule) ValidateStored() error {
	return r.validateStructural()
}

// validateStructural holds the storage-integrity checks shared by both validation
// profiles (Validate / ValidateStored). A violation here means the rule is
// genuinely malformed (not merely authored against a write-side policy), so both
// the write path and the read path must reject it.
//
//   - ID must be non-empty and must not use the reserved '_' prefix (framework
//     decision-attribution sentinels; a stored row spoofing it would make
//     matched_rule_id attribution ambiguous, so this stays strict on read too).
//   - Name must be non-empty.
//   - Effect must be a valid authz.Effect.
//   - Each Condition (if any) must pass Condition.Validate().
//   - Obligations must pass Obligations.Validate().
func (r Rule) validateStructural() error {
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
