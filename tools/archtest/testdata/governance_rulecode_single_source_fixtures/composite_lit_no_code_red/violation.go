// Package composite_lit_no_code_red is a testdata fixture for INV-2
// (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01) negative case.
//
// Shape tested: ValidationResult{Severity: SeverityError, Message: "..."} —
// a struct literal that omits the Code: field entirely. Without the
// completeness check, the existing key-loop sees no Code key and silently
// passes; the violation surfaces as RuleCode("") at runtime, which never
// matches a rulecodes.go const.
//
// Expected: 1 violation from the CompositeLit scan path.
package composite_lit_no_code_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violateNoCode returns a ValidationResult that omits Code:. The receiver of
// every governance result must reference a RuleCode const declared in
// rulecodes.go; the zero value of RuleCode bypasses the single-source funnel.
func violateNoCode() gov.ValidationResult {
	return gov.ValidationResult{
		Severity: gov.SeverityError,
		Message:  "missing code; fix: add a RuleCode reference",
	}
}
