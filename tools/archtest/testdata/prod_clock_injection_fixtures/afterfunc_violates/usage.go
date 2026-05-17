// Package afterfunc_violates verifies that time.AfterFunc is flagged:
// 1 violation expected (declared via spec.Violation()).
package afterfunc_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func schedule(d time.Duration, fn func()) *time.Timer {
	spec.Violation()
	return time.AfterFunc(d, fn)
}
