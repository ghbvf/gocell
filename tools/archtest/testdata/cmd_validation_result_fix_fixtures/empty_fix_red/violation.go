// Package empty_fix_red is a testdata fixture for CMD-VALIDATIONRESULT-FIX-FIELD-01
// negative case — the empty Fix field scan path.
//
// Shape tested: a governance.ValidationResult{} composite literal that sets Fix
// to an empty string literal "". The archtest must flag this because an empty
// literal carries no remediation guidance.
//
// This fixture uses the REAL governance.ValidationResult type (cross-package
// import), so the type-resolution gate via go/types is exercised precisely as
// it would be in production cmd/gocell/app code.
//
// Expected: 1 violation — Fix: "" (empty literal).
package empty_fix_red

import "github.com/ghbvf/gocell/kernel/governance"

// emptyFixResult constructs a ValidationResult with an explicit empty Fix field.
// CMD-VALIDATIONRESULT-FIX-FIELD-01 must flag this.
func emptyFixResult() governance.ValidationResult {
	return governance.ValidationResult{
		Code:      governance.RuleCode("CHECK-FIXTURE-EMPTY-FIX"),
		Severity:  governance.SeverityError,
		IssueType: governance.IssueRequired,
		Scope:     "fixture",
		Message:   "fixture result with empty Fix field",
		Fix:       "",
	}
}
