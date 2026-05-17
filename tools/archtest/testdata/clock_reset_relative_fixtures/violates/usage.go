// Package violates demonstrates prod code calling Timer.Reset(d time.Duration),
// which KERNEL-CLOCK-RESET-RELATIVE-PROD-01 must flag.
// 1 violation expected (declared via spec.Violation()).
package violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

// Timer mirrors the kernel/clock.Timer interface so the fixture is self-contained.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
	ResetAt(deadline time.Time) bool
}

// rearm calls the relative Reset — this is the violation.
func rearm(t Timer, d time.Duration) bool {
	spec.Violation()
	return t.Reset(d) // KERNEL-CLOCK-RESET-RELATIVE-PROD-01 violation
}
