// Package missing_fix_red is a testdata fixture for CMD-VALIDATIONRESULT-FIX-FIELD-01
// negative case — the missing Fix field scan path.
//
// Shape tested: a governance.ValidationResult{} composite literal that does not
// set the Fix: field at all. The archtest must flag this even though it is a
// named-field composite literal that sets Code, Severity, and Message.
//
// This fixture uses the REAL governance.ValidationResult type (cross-package
// import), so the type-resolution gate via go/types is exercised precisely as
// it would be in production cmd/gocell/app code.
//
// Expected: 1 violation — Fix: field absent.
package missing_fix_red

import "github.com/ghbvf/gocell/framework/kernel/governance"

// missingFixResult constructs a ValidationResult without a Fix field.
// CMD-VALIDATIONRESULT-FIX-FIELD-01 must flag this.
func missingFixResult() governance.ValidationResult {
	return governance.ValidationResult{
		Code:      governance.RuleCode("CHECK-FIXTURE-MISSING-FIX"),
		Severity:  governance.SeverityError,
		IssueType: governance.IssueInvalid,
		Scope:     "fixture",
		Message:   "fixture result with no Fix field",
	}
}
