package authz

import "github.com/ghbvf/gocell/framework/pkg/errcode"

// Effect is the authorization verdict enum. iota+1 zero-invalid convention
// (mirroring kernel/saga.Status, kernel/outbox.Disposition, pkg/tenant.RowScope)
// ensures the zero value is never mistaken for a valid effect.
//
// XACML: Permit/Deny; Cedar: permit/forbid.
type Effect uint8

const (
	// EffectAllow permits the requested action. Corresponds to XACML Permit /
	// Cedar permit.
	EffectAllow Effect = iota + 1
	// EffectDeny forbids the requested action. Corresponds to XACML Deny /
	// Cedar forbid. Deny-overrides (XACML §7.16) means any EffectDeny wins
	// over any number of EffectAllow results.
	EffectDeny
)

// Valid reports whether e is one of the two defined effects (EffectAllow or
// EffectDeny). The zero value returns false.
func (e Effect) Valid() bool {
	return e == EffectAllow || e == EffectDeny
}

// String returns the wire/log spelling of the effect, or "invalid" for the
// zero value and any out-of-range value.
func (e Effect) String() string {
	switch e {
	case EffectAllow:
		return "allow"
	case EffectDeny:
		return "deny"
	default:
		return "invalid"
	}
}

// Validate returns an error for the zero value or any out-of-range value.
func (e Effect) Validate() error {
	if !e.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "authz: invalid Effect value")
	}
	return nil
}

// ParseEffect converts a wire/log spelling back to its Effect value (inverse of
// String). Recognized codes: "allow" → EffectAllow, "deny" → EffectDeny. Any
// other input — including the empty string — returns an error (fail-closed).
func ParseEffect(s string) (Effect, error) {
	switch s {
	case "allow":
		return EffectAllow, nil
	case "deny":
		return EffectDeny, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "authz: unknown Effect wire code")
	}
}
