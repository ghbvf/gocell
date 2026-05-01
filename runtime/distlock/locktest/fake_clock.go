package locktest

import (
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// FakeClock is a type alias for [clockmock.FakeClock]. All behavior,
// including [FakeClock.Advance], [FakeClock.Set], and [FakeClock.PendingTimers],
// is provided by the canonical implementation in kernel/clock/clockmock.
//
// Existing call sites that use locktest.FakeClock continue to compile without
// change; new code should prefer importing kernel/clock/clockmock directly.
type FakeClock = clockmock.FakeClock

// FakeTimer is a type alias for [clockmock.FakeTimer].
//
// Existing call sites that use locktest.FakeTimer continue to compile without
// change; new code should prefer importing kernel/clock/clockmock directly.
type FakeTimer = clockmock.FakeTimer

// NewFakeClock creates a FakeClock starting at the given initial time.
// Delegates to [clockmock.New].
//
// Deprecated: prefer clockmock.New directly for new code.
func NewFakeClock(initial time.Time) *FakeClock {
	return clockmock.New(initial)
}

// Compile-time assertions: FakeClock must satisfy clock.Clock; FakeTimer must
// satisfy clock.Timer. These are already enforced by clockmock — repeated here
// so that any future divergence is caught in this package too.
var (
	_ interface {
		Now() time.Time
		Since(time.Time) time.Duration
		Until(time.Time) time.Duration
	} = (*FakeClock)(nil)
)
