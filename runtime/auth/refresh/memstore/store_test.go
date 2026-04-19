package memstore_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// baseTime is the synthetic epoch for all FakeClocks in this test file.
var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// TestMemStoreContract runs the full C1-C7 contract test suite (T1-T12) against
// the in-memory store. In the TDD red phase the memstore stubs return nil/nil,
// so the suite is expected to fail on most sub-tests.
func TestMemStoreContract(t *testing.T) {
	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		clock := storetest.NewFakeClock(baseTime)
		// Pass nil for rand so memstore falls back to crypto/rand (fine for
		// the red phase; B3 may inject a deterministic reader for hermeticity).
		return memstore.New(policy, clock, nil), clock
	})
}
