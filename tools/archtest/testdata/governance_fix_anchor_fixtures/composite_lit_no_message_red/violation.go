// Package composite_lit_no_message_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01) negative case.
//
// Shape tested: ValidationResult{Severity: SeverityError, ...} that omits
// the Message: field entirely. Without the completeness check, the legacy
// key loop saw msgExpr == nil and silently returned; the violation is now
// surfaced because a missing Message can never carry the "; fix:" anchor.
//
// Expected: 1 violation from the CompositeLit scan path.
package composite_lit_no_message_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violateNoMessage returns an error result without a Message field. Every
// SeverityError result must declare its own actionable Message; omitting it
// silently drops the remediation guidance from the rule output.
func violateNoMessage() gov.ValidationResult {
	return gov.ValidationResult{
		Severity: gov.SeverityError,
		Field:    "field.path",
	}
}
