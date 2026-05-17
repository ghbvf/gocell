// Package addition_violates verifies that an addition of two literal durations
// produces two violations (each operand is an independent matching expression):
// 2 violations expected (declared via spec.Violation()).
package addition_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() {
	spec.Violation()
	spec.Violation()
}

var defaultWindow = 5*time.Second + 30*time.Millisecond
