// Package func_value_ref_violates verifies that taking time.Now as a
// function value (no parens) is flagged. This is the pattern used by the
// `now func() time.Time` field convention; without SelectorExpr scan the
// value reference would slip through. 1 violation expected.
package func_value_ref_violates

import "time"

var now = time.Now

func current() time.Time {
	return now()
}
