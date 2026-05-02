// Package compliant demonstrates prod code calling Timer.ResetAt(deadline time.Time),
// which KERNEL-CLOCK-RESET-RELATIVE-PROD-01 must not flag.
package compliant

import "time"

// Timer mirrors the kernel/clock.Timer interface so the fixture is self-contained.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
	ResetAt(deadline time.Time) bool
}

// rearm calls the absolute ResetAt — this is the correct prod pattern.
func rearm(t Timer, deadline time.Time) bool {
	return t.ResetAt(deadline)
}
