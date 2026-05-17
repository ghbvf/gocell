// Package non_whitelist_sink_violates verifies that passing a literal duration
// to a non-standard (non-whitelist) function is still caught by the type-based
// gate: 1 violation expected (declared via spec.Violation()).
package non_whitelist_sink_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func myHelper(d time.Duration) {}

func f() {
	spec.Violation()
	myHelper(5 * time.Second)
}
