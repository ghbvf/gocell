package session_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// memBenchFactory is the storetest.BenchFactory adapter for the in-memory
// Store implementation. Each call returns a freshly constructed MemStore +
// FakeClock anchored at storetest.EpochAnchor(); cleanup is a no-op.
func memBenchFactory(b *testing.B) (session.Store, *clockmock.FakeClock, func()) {
	b.Helper()
	fc := clockmock.New(storetest.EpochAnchor())
	store, err := session.NewMemStore(storetest.NewBenchProtocol(b), fc)
	if err != nil {
		b.Fatalf("memBenchFactory: NewMemStore: %v", err)
	}
	return store, fc, func() {}
}

// BenchmarkMemStore_Suite drives the canonical bench suite against MemStore so
// PG-backed adapters have a shared baseline (PR444-FU-SESSIONSTORE-BENCH-01).
func BenchmarkMemStore_Suite(b *testing.B) {
	storetest.Bench(b, memBenchFactory, storetest.NewBenchProtocol(b))
}
