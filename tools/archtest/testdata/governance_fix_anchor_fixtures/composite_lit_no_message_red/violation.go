// Package composite_lit_no_message_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the construction-funnel
// scan path.
//
// Shape tested: a raw ValidationResult{} composite literal that omits most
// fields. Any raw composite outside locator.go is forbidden (it bypasses the
// constructor funnel and the mandatory typed Fix), regardless of which fields
// it sets. Expected: 1 violation from the funnel scan path.
package composite_lit_no_message_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violateNoMessage constructs a raw ValidationResult composite literal — the
// forbidden bypass shape.
func violateNoMessage() gov.ValidationResult {
	return gov.ValidationResult{
		Severity: gov.SeverityError,
		Field:    "field.path",
	}
}
