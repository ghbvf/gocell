// Package time_now_add_literal_violates verifies that time.Now().Add(literal)
// is caught (the literal inside Add is a time.Duration expression):
// 1 violation expected (declared via spec.Violation()).
package time_now_add_literal_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func cutoff() time.Time {
	spec.Violation()
	return time.Now().Add(5 * time.Second)
}
