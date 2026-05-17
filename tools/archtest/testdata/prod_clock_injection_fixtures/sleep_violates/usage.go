// Package sleep_violates verifies that time.Sleep is flagged:
// 1 violation expected (declared via spec.Violation()).
package sleep_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func nap(d time.Duration) {
	spec.Violation()
	time.Sleep(d)
}
