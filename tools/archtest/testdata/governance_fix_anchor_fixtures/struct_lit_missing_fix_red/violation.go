// Package struct_lit_missing_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the construction-funnel
// scan path.
//
// Shape tested: a raw ValidationResult{} composite literal with named fields.
// Such literals are forbidden outside locator.go because they bypass the
// newError / newWarning / newErrorAt / newScopedError funnel (and thus the
// mandatory typed Fix). Expected: 1 violation from the funnel scan path.
package struct_lit_missing_fix_red

import gov "github.com/ghbvf/gocell/framework/kernel/governance"

// violateStructLit constructs a raw ValidationResult composite literal — the
// forbidden bypass shape.
func violateStructLit() gov.ValidationResult {
	return gov.ValidationResult{
		Severity: gov.SeverityError,
		Message:  "doc naming guard is required for strict validation",
	}
}
