// Package orphan_detection_red is a testdata fixture for INV-1
// (GOVERNANCE-RULES-REGISTRATION-GUARD-01) negative case.
//
// Shape tested: a rule-shaped method validateOrphan is declared on *Validator
// but is NOT referenced in allRules. The orphan-detection property of
// extractRegisteredFromAllRules must surface it as "declared but not
// registered".
//
// Expected: setDifference(declared, registered) contains "validateOrphan".
// This proves the check is ACTIVE: if the scan only read one side, the orphan
// would be invisible.
package orphan_detection_red

// ValidationResult mirrors kernel/governance.ValidationResult shape so that
// ruleShapeSignature accepts the return type by name ("ValidationResult").
type ValidationResult struct{}

// Validator is the registration anchor — equivalent to kernel/governance.Validator.
type Validator struct{}

// Rule is a minimal Rule struct whose Detect field matches the production
// func(v *Validator) []ValidationResult signature.
type Rule struct {
	Detect func(v *Validator) []ValidationResult
}

// validateRegistered is a rule-shaped method that IS referenced in allRules.
func (v *Validator) validateRegistered() []ValidationResult { return nil }

// validateOrphan is a rule-shaped method that is NOT referenced in allRules.
// It is the orphan the test asserts appears in the "declared but not registered" set.
func (v *Validator) validateOrphan() []ValidationResult { return nil }

// allRules is the package-level registration slice, referencing only
// validateRegistered. validateOrphan is intentionally omitted to create the
// orphan condition that INV-1 must detect.
var allRules = []Rule{
	{Detect: (*Validator).validateRegistered},
}
