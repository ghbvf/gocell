// Package composite_lit_positional_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the construction-funnel
// scan path.
//
// Shape tested: a raw ValidationResult{} composite literal with positional
// fields. Any raw composite outside locator.go is forbidden (it bypasses the
// constructor funnel and the mandatory typed Fix); the positional form is
// caught by the same type-gated composite scan as the named form.
//
// Expected: 1 violation from the funnel scan path.
package composite_lit_positional_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violatePositional constructs a raw ValidationResult composite literal with
// positional fields — the forbidden bypass shape. The 10 values match the
// field order Code, Severity, IssueType, File, Scope, Field, Message, Fix,
// Line, Column.
func violatePositional() gov.ValidationResult {
	return gov.ValidationResult{
		gov.RuleCode("X-99"),
		gov.SeverityError,
		gov.IssueForbidden,
		"fixture.yaml",
		"",
		"field.path",
		"problem statement",
		"",
		0,
		0,
	}
}
