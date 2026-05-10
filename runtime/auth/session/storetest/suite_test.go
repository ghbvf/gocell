package storetest_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// memFactory is the storetest.Factory for MemStore. It anchors a fresh
// FakeClock at storetest.EpochAnchor() so case timestamps are deterministic
// and aligned with NewSessionFixture.
//
// Living here (not in mem_store_test.go) makes storetest itself the
// authoritative self-test of the conformance suite — per-package coverage
// counters now attribute the suite's executed lines to runtime/auth/session/
// storetest/, which is what SonarCloud reads.
func memFactory(t *testing.T) (session.Store, *clockmock.FakeClock, func()) {
	t.Helper()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	if err != nil {
		t.Fatalf("memFactory: NewMemStore failed: %v", err)
	}
	return store, fc, func() {}
}

// TestSuite_AgainstMemStore exercises the full Protocol-driven contract
// suite against MemStore. It serves two purposes:
//   - asserts MemStore conforms to the Store contract (functional test)
//   - exercises every helper in suite.go so storetest's per-package coverage
//     reflects the work the suite actually does (sonar gate)
func TestSuite_AgainstMemStore(t *testing.T) {
	t.Parallel()
	storetest.Run(t, memFactory, storetest.NewTestProtocol(t))
}
