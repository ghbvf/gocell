// Package chained_unit_violates verifies that a chained magnitude expression
// like 7*24*time.Hour is caught as one violation:
// 1 violation expected (declared via spec.Violation()).
package chained_unit_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() { spec.Violation() }

var defaultRetention = 7 * 24 * time.Hour
