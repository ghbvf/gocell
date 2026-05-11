package storetest_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/audit/ledger/storetest"
)

// TestSuite_MemStore runs the full contract suite against the in-memory store
// implementation, proving it satisfies all ledger.Store invariants.
func TestSuite_MemStore(t *testing.T) {
	protocol := storetest.NewTestProtocol(t)

	factory := func(t *testing.T) (ledger.Store, *clockmock.FakeClock, func()) {
		t.Helper()
		fc := clockmock.New(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		store, err := ledger.NewMemStore(protocol, fc)
		if err != nil {
			t.Fatalf("NewMemStore: %v", err)
		}
		return store, fc, func() {}
	}

	storetest.Run(t, factory, protocol)
}
