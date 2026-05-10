//go:build integration

package postgres

import (
	"context"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// setupPostgresForBench starts a PostgreSQL testcontainer for use in benchmark
// tests. It mirrors setupPostgres but bridges *testing.B to the container
// setup by wrapping the call inside a synthetic *testing.T obtained via
// b.Run (avoids the *testing.T vs *testing.B interface mismatch in
// testutil.RequireDocker which requires *testing.T).
//
// The returned cleanup func terminates the container and closes the pool.
func setupPostgresForBench(b *testing.B) (*Pool, func()) {
	b.Helper()

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		b.Skipf("setupPostgresForBench: Docker unavailable, skipping: %v", err)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		b.Fatalf("setupPostgresForBench: connection string: %v", err)
	}

	pool, err := NewPool(ctx, Config{DSN: connStr})
	if err != nil {
		_ = container.Terminate(ctx)
		b.Fatalf("setupPostgresForBench: NewPool: %v", err)
	}

	cleanup := func() {
		_ = pool.Close(ctx)
		if err := container.Terminate(ctx); err != nil {
			b.Logf("WARN: failed to terminate container: %v", err)
		}
	}
	return pool, cleanup
}

// pgBenchFactory is the storetest.BenchFactory for the PG session store.
//
// Each call starts a fresh testcontainer, applies migrations, and returns a
// PGSessionStore wired with a FakeClock anchored at storetest.EpochAnchor().
// The cleanup func truncates sessions + users and terminates the container.
//
// The pgSessionStoreWrapper (defined in session_store_integration_test.go)
// bridges storetest's TEXT subjectIDs to the UUID FK in the sessions table.
func pgBenchFactory(b *testing.B) (session.Store, *clockmock.FakeClock, func()) {
	b.Helper()

	pool, teardown := setupPostgresForBench(b)

	ctx := context.Background()
	migrator, err := NewMigrator(pool, testMigrationsFS(b), "schema_migrations")
	if err != nil {
		teardown()
		b.Fatalf("pgBenchFactory: NewMigrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		teardown()
		b.Fatalf("pgBenchFactory: migrator.Up: %v", err)
	}

	fc := clockmock.New(storetest.EpochAnchor())
	txm := NewTxManager(pool)
	proto := storetest.NewBenchProtocol(b)
	store, err := NewSessionStore(pool.DB(), txm, proto, fc)
	if err != nil {
		teardown()
		b.Fatalf("pgBenchFactory: NewSessionStore: %v", err)
	}

	// pgSessionStoreWrapper defined in session_store_integration_test.go bridges
	// TEXT subjectIDs → UUID FK. wrapper.t is testing.TB so *testing.B works.
	wrapper := &pgSessionStoreWrapper{inner: store, pool: pool, t: b}

	cleanup := func() {
		_, _ = pool.DB().Exec(context.Background(),
			"TRUNCATE sessions, users RESTART IDENTITY CASCADE")
		teardown()
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
	storetest.Bench(b, pgBenchFactory, storetest.NewBenchProtocol(b))
}
