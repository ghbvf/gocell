// Package composite_lit_positional_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01) negative case.
//
// Shape tested: positional ValidationResult literal with SeverityError +
// a message that lacks "; fix:". The legacy KeyValueExpr loop never inspects
// positional elements, so neither the SeverityError detection nor the
// fix-anchor check fires. The positional ban catches the shape independent
// of either downstream check.
//
// Expected: 1 violation from the CompositeLit scan path.
package composite_lit_positional_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violatePositional constructs a ValidationResult with positional fields,
// bypassing both the SeverityError detection and the fix-anchor check that
// rely on KeyValueExpr enumeration.
func violatePositional() gov.ValidationResult {
	return gov.ValidationResult{
		gov.RuleCode("X-99"),
		gov.SeverityError,
		gov.IssueForbidden,
		"fixture.yaml",
		"",
		"field.path",
		"no fix anchor",
		0,
		0,
	}
}
