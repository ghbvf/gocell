// Package composite_lit_positional_red is a testdata fixture for INV-2
// (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01) negative case.
//
// Shape tested: positional ValidationResult literal. Even when Code is
// supplied as the first positional argument, the existing key-loop only
// inspects KeyValueExpr children — positional elements skip the entire
// Code: identity check. The positional ban makes this shape an explicit
// violation independent of the Code value.
//
// Expected: 1 violation from the CompositeLit scan path.
package composite_lit_positional_red

import gov "github.com/ghbvf/gocell/kernel/governance"

// violatePositional constructs a ValidationResult with positional fields.
// The Code value is a bare RuleCode conversion that bypasses the
// rulecodes.go single-source funnel; the positional ban catches the shape
// before the Code identity check would otherwise be applied.
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
