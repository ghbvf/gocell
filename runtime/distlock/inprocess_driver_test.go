package distlock_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestInProcessDriver_DriverConformance enrolls the production in-process Driver
// in the canonical semantic conformance suite (C-1..C-4, C-7). The in-process
// Driver is the single-pod backend the saga-projection Tailer's leader gate uses
// in demo/single-pod topology (the Tailer requires a non-nil distlock.Locker);
// it MUST behave identically to the Redis backend for token-ownership semantics.
//
// TTL physics (RunDriverTTLConformance, C-5/C-6) are NOT run here: the in-process
// Driver's expiry is clock-injected (clock.Clock), exercised via the manager's
// FakeClock-driven renew cycles, exactly like locktest.FakeDriver.
func TestInProcessDriver_DriverConformance(t *testing.T) {
	t.Parallel()
	locktest.RunDriverConformance(t, func(t *testing.T) distlock.Driver {
		t.Helper()
		// A clock that does not advance keeps the minute-long conformance TTLs
		// live for the duration of each sub-case (no spurious expiry).
		clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		return distlock.NewInProcessDriver(clk)
	})
}
