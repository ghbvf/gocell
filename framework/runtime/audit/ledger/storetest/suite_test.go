package storetest_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger/storetest"
)

// TestSuite_MemStore runs the full contract suite against the in-memory store
// implementation, proving it satisfies all ledger.Store invariants.
func TestSuite_MemStore(t *testing.T) {
	protocol := storetest.NewTestProtocol(t)

	factory := func(t *testing.T) (ledger.Store, persistence.TxRunner, *clockmock.FakeClock, func()) {
		t.Helper()
		fc := clockmock.New(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		store, err := ledger.NewMemStore(protocol, fc)
		if err != nil {
			t.Fatalf("NewMemStore: %v", err)
		}
		return store, storetest.PassthroughTxRunner(), fc, func() {}
	}

	storetest.Run(t, factory, protocol)
}

// FuzzEntryRoundTrip runs the property-based Entry round-trip + HMAC parity
// fuzz against the in-memory store. The store and protocol are built once and
// reused across iterations; seed corpus + fuzz body live in
// storetest.RunEntryRoundTripFuzz so the PG integration target shares them.
func FuzzEntryRoundTrip(f *testing.F) {
	protocol := storetest.NewTestProtocol(f)
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := ledger.NewMemStore(protocol, fc)
	if err != nil {
		f.Fatalf("NewMemStore: %v", err)
	}
	storetest.RunEntryRoundTripFuzz(f, store, protocol, storetest.PassthroughTxRunner())
}
