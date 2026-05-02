// Package alias_violates verifies that an aliased time import does not
// bypass the gate (resolution is type-driven, not identifier-name-based):
// 1 violation expected.
package alias_violates

import wallclock "time"

func now() wallclock.Time {
	return wallclock.Now()
}
