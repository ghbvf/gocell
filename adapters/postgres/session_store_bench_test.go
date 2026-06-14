//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	"github.com/ghbvf/gocell/framework/runtime/auth/session/storetest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// pgBenchFactory is the storetest.BenchFactory for the PG session store.
//
// storetest.Bench's benchRevokeForSubject calls the factory inside the b.N
// loop, so each call mints a fresh per-test DB and the returned cleanup must
// release it per-iteration. It uses sharedPG.CloneManaged (NOT migratedPool):
// migratedPool registers a t.Cleanup(drop) that defers to benchmark end, which
// would accumulate b.N databases + connection pools and exhaust PG. CloneManaged
// returns a manual release that cleanup invokes each iteration.
//
// The pgSessionStoreWrapper (defined in session_store_integration_test.go)
// bridges storetest's TEXT subjectIDs to the UUID FK in the sessions table.
func pgBenchFactory(b *testing.B) (session.Store, *clockmock.FakeClock, func()) {
	b.Helper()
	testutil.RequireDocker(b)

	dsn, releaseDB := sharedPG.CloneManaged(b)
	pool, err := NewPool(context.Background(), Config{DSN: dsn})
	if err != nil {
		releaseDB()
		b.Fatalf("pgBenchFactory: open pool: %v", err)
	}

	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(pool)
	proto := storetest.NewBenchProtocol(b)
	store, err := NewSessionStore(pool.DB(), txm, proto, fc)
	if err != nil {
		_ = pool.Close(context.Background())
		releaseDB()
		b.Fatalf("pgBenchFactory: NewSessionStore: %v", err)
	}

	// pgSessionStoreWrapper defined in session_store_integration_test.go bridges
	// TEXT subjectIDs → UUID FK. wrapper.t is testing.TB so *testing.B works.
	wrapper := &pgSessionStoreWrapper{inner: store, pool: pool, t: b}

	cleanup := func() {
		_ = pool.Close(context.Background())
		releaseDB()
	}
	return wrapper, fc, cleanup
}

// BenchmarkPGSessionStore drives the canonical session.Store benchmark suite
// against the PG backend so micro-benchmarks stay comparable to MemStore
// (PR444-FU-SESSIONSTORE-BENCH-01). Subtests:
//
//	BenchmarkPGSessionStore/RevokeForSubject_1000 — credential-event revoke fan-out
//	BenchmarkPGSessionStore/MixedConcurrent       — login/validate/logout interleave
func BenchmarkPGSessionStore(b *testing.B) {
	storetest.Bench(b, pgBenchFactory)
}
