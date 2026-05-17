// Package alias_violates verifies that an aliased time import does not
// bypass the gate (resolution is type-driven, not identifier-name-based):
// 1 violation expected (declared via spec.Violation()).
package alias_violates

import (
	wallclock "time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func now() wallclock.Time {
	spec.Violation()
	return wallclock.Now()
}
