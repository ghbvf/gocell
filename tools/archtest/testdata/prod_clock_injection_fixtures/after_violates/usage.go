// Package after_violates verifies that time.After is flagged:
// 1 violation expected (declared via spec.Violation()).
package after_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func block(d time.Duration) {
	spec.Violation()
	<-time.After(d)
}
