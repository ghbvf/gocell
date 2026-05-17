// Package since_violates is a fixture for archtest negative case:
// verifies that time.Since is flagged:
// 1 violation expected (declared via spec.Violation()).
package since_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func elapsed(t time.Time) time.Duration {
	spec.Violation()
	return time.Since(t)
}
