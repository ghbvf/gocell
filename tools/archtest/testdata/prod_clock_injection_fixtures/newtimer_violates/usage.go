// Package newtimer_violates is a fixture for archtest negative case:
// verifies that time.NewTimer is flagged:
// 1 violation expected (declared via spec.Violation()).
package newtimer_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func wait(d time.Duration) {
	spec.Violation()
	t := time.NewTimer(d)
	defer t.Stop()
	<-t.C
}
