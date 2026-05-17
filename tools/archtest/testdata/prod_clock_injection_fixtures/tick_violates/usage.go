// Package tick_violates verifies that time.Tick (the leaky shortcut form)
// is flagged: 1 violation expected (declared via spec.Violation()).
package tick_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func leak(interval time.Duration) <-chan time.Time {
	spec.Violation()
	return time.Tick(interval)
}
