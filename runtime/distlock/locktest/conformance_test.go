package locktest_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestFakeDriver_Conformance runs the semantic correctness conformance suite
// against the in-memory FakeDriver. TTL physics (C-5, C-6) are deliberately
// excluded here because FakeDriver's TTL expiry is clock-injected and exercised
// via the manager's FakeClock-driven renew cycles, not real time.Sleep.
//
// Integration tests in adapters/redis/integration_test.go call both
// RunDriverConformance and RunDriverTTLConformance against a real Redis backend.
func TestFakeDriver_Conformance(t *testing.T) {
	// Each sub-test gets a fresh FakeDriver wired to a shared FakeClock so
	// TTL expiry is deterministic (no real-time dependency).
	fc := locktest.NewFakeClock(time.Time{})
	locktest.RunDriverConformance(t, func(t *testing.T) distlock.Driver {
		return locktest.NewFakeDriverWithClock(fc.Now)
	})
}
