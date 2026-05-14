// Package struct_lit_missing_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01) negative case.
//
// Shape tested: ValidationResult{Severity: SeverityError, Message: <no fix anchor>}
// Expected: 1 violation from the CompositeLit scan path.
package struct_lit_missing_fix_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violateStructLit constructs a ValidationResult with SeverityError but a
// message that lacks the required "; fix:" anchor.
func violateStructLit() gov.ValidationResult {
	return gov.ValidationResult{
		Severity: gov.SeverityError,
		Message:  "doc naming guard is required for strict validation",
	}
}
