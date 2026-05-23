//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// pgBenchFactory is the storetest.BenchFactory for the PG session store.
//
// Each call uses migratedPool(b) to get a fresh per-test DB (production
// schema already present). The cleanup func truncates sessions + users.
//
// The pgSessionStoreWrapper (defined in session_store_integration_test.go)
// bridges storetest's TEXT subjectIDs to the UUID FK in the sessions table.
func pgBenchFactory(b *testing.B) (session.Store, *clockmock.FakeClock, func()) {
	b.Helper()
	testutil.RequireDocker(b)

	pool := migratedPool(b)

	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(pool)
	proto := storetest.NewBenchProtocol(b)
	store, err := NewSessionStore(pool.DB(), txm, proto, fc)
	if err != nil {
		b.Fatalf("pgBenchFactory: NewSessionStore: %v", err)
	}

	// pgSessionStoreWrapper defined in session_store_integration_test.go bridges
	// TEXT subjectIDs → UUID FK. wrapper.t is testing.TB so *testing.B works.
	wrapper := &pgSessionStoreWrapper{inner: store, pool: pool, t: b}

	cleanup := func() {
		_, _ = pool.DB().Exec(context.Background(),
			"TRUNCATE sessions, users RESTART IDENTITY CASCADE")
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
