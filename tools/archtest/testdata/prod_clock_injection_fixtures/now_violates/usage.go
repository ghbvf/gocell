// Package now_violates is a fixture for archtest negative case:
// verifies that time.Now is flagged:
// 1 violation expected (declared via spec.Violation()).
package now_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func currentTime() time.Time {
	spec.Violation()
	return time.Now()
}
