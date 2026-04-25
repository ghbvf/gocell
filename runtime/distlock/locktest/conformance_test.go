package locktest_test

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/distlock"
	"github.com/ghbvf/gocell/runtime/distlock/locktest"
)

// TestFakeDriver_Conformance runs the full Driver conformance suite against
// the in-memory FakeDriver. Any Driver implementation must pass these tests.
func TestFakeDriver_Conformance(t *testing.T) {
	locktest.RunDriverConformance(t, func(t *testing.T) distlock.Driver {
		return locktest.NewFakeDriver()
	})
}
