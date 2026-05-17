// Package switch_case_violates verifies that a literal duration in a switch
// case expression is caught: 1 violation expected (declared via spec.Violation()).
package switch_case_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func classify(d time.Duration) string {
	spec.Violation()
	switch d {
	case 5 * time.Second:
		return "short"
	default:
		return "other"
	}
}
