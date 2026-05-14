// Package shadow_receiver_red is a testdata fixture for INV-1
// (GOVERNANCE-RULES-REGISTRATION-GUARD-01) negative case.
//
// Shape tested: rules() returns []func(){ o.validateFOO } where o is an
// instance of OtherType, NOT *Validator. A name-only check would accept
// validateFOO as registered; the receiver-type check must reject it because
// OtherType.validateFOO never runs through Validator.rules().
//
// Expected: registeredElementMethodName returns ("", false) for the
// o.validateFOO selector when called with this fixture's TypesInfo and the
// fixture's *Validator named type.
package shadow_receiver_red

// ValidationResult mirrors kernel/governance.ValidationResult shape so the
// rules() return type compiles.
type ValidationResult struct{}

// Validator is the legitimate registration anchor — equivalent to
// kernel/governance.Validator.
type Validator struct{}

// validateLegit is a real *Validator method; the receiver-type check must
// accept this one.
func (v *Validator) validateLegit() []ValidationResult { return nil }

// OtherType is a sibling type that exposes a method with the validate*
// name shape but is NOT *Validator. Including it in rules() must be
// rejected by the receiver-type check (the actual call site runs
// OtherType.validateFOO, which leaves Validator.validateLegit silently
// unregistered).
type OtherType struct{}

// validateFOO has the rule signature but lives on the wrong receiver.
func (o *OtherType) validateFOO() []ValidationResult { return nil }

// rules constructs the slice that an attacker (or a careless refactor)
// might write — mixing a real Validator method with a shadow OtherType
// method. The receiver-type check must accept v.validateLegit and reject
// o.validateFOO.
func (v *Validator) rules() []func() []ValidationResult {
	o := &OtherType{}
	return []func() []ValidationResult{
		v.validateLegit,
		o.validateFOO,
	}
}
