// Package switch_case_violates verifies that a literal duration in a switch
// case expression is caught: 1 violation expected.
package switch_case_violates

import "time"

func classify(d time.Duration) string {
	switch d {
	case 5 * time.Second:
		return "short"
	default:
		return "other"
	}
}
