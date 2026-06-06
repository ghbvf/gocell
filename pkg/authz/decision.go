package authz

// Decision is the sealed authorization verdict returned by a PDP (auth.Authorizer).
//
// All fields are unexported: the ONLY way to produce a Decision is Allow() /
// Deny(), so a business handler cannot forge `authz.Decision{Effect: EffectAllow}`
// (compile error outside this package) — a P0 authz-bypass vector this seal closes.
//
// The zero value (Decision{}) has Effect() that is NOT EffectAllow → consumers
// testing d.IsAllow() fail closed on a zero value.
//
// FR-019: a Decision never crosses an async boundary; with no exported fields and
// no Unmarshal this type is structurally non-serializable, reinforcing that
// invariant.
//
// # Combining Algorithm
//
// Deny-overrides (XACML §7.16) / forbid-wins (Cedar): any EffectDeny result
// wins over any number of EffectAllow results. This semantics is DOCUMENTED
// here but ENFORCEMENT (an evaluator that applies deny-overrides) lands in PR-7.
//
// # AI-robust Grade
//
// Upstream Hard: sealed unexported fields make outside-package struct-literal
// construction a compile error. This is the "sealed construction" range item from
// the ai-robust.md Hard 范本目录.
//
// Downstream caller-allowlist: deferred to PR-7 (#1345) — vacuous today because
// there is no production Allow()/Deny() caller yet.
type Decision struct {
	effect      Effect
	obligations Obligations
	reason      string
}

// Allow returns a permit Decision carrying obligations the PEP must discharge.
// The obligations are only meaningful when IsAllow() == true; PEPs SHOULD NOT
// enforce Obligations from a Deny Decision.
func Allow(o Obligations) Decision {
	return Decision{effect: EffectAllow, obligations: o}
}

// Deny returns a fail-closed deny Decision. reason is an opaque, server-side
// diagnostic string intended for logging (per observability rules). The
// framework does not interpret its structure and it intentionally does NOT
// carry accesscore's PolicyID type — keeping pkg/authz free of accesscore
// dependencies.
func Deny(reason string) Decision {
	return Decision{effect: EffectDeny, reason: reason}
}

// Effect reports the verdict. The zero-value Decision returns a non-Allow
// effect (fail-closed): any consumer testing d.Effect() == EffectAllow will
// deny access on a zero Decision.
func (d Decision) Effect() Effect {
	return d.effect
}

// Obligations returns the mandatory obligations the PEP must enforce when the
// Decision is Allow. Obligations are meaningless on a Deny Decision.
func (d Decision) Obligations() Obligations {
	return d.obligations
}

// Reason returns the opaque server-side diagnostic string supplied to Deny().
// Empty string on an Allow Decision. Intended for logging only.
func (d Decision) Reason() string {
	return d.reason
}

// IsAllow reports whether the decision permits the action. This is the
// idiomatic check for PEP enforcement code:
//
//	if !dec.IsAllow() {
//	    return nil, ErrForbidden
//	}
//
// Returns false for the zero-value Decision (fail-closed).
func (d Decision) IsAllow() bool {
	return d.effect == EffectAllow
}
