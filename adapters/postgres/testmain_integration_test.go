//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgclone"
)

// sharedPG owns one PostgreSQL container for the whole adapters/postgres test
// binary. The migrated template carries the production migration set; per-test
// databases are cloned (migratedPool) or created empty (emptyPool) at
// ~10-200ms each, replacing the historic ~2.5s-per-test container spin-up
// (~93 spin-ups, ~237s) that dominated CI.
//
// adapters/postgres integration tests are white-box `package postgres`, so
// they cannot import pgshare (pgshare imports adapterpg → import cycle). They
// use pgclone directly with this in-package migration callback. pgclone is the
// single sanctioned holder of tcpostgres.Run (PG-TESTCONTAINER-FUNNEL-01).
var sharedPG = pgclone.New("gocell_adapters_postgres_test_template",
	func(ctx context.Context, dsn string) (err error) {
		pool, err := NewPool(ctx, Config{DSN: dsn})
		if err != nil {
			return fmt.Errorf("open template pool: %w", err)
		}
		defer func() {
			// Close error matters: a lingering connection on the template blocks
			// the first CREATE DATABASE ... TEMPLATE clone. Surface it (when the
			// migration itself succeeded) so boot fails fast with a clear cause.
			if cerr := pool.Close(ctx); cerr != nil && err == nil {
				err = fmt.Errorf("close migration pool: %w", cerr)
			}
		}()
		fsys, err := MigrationsFS()
		if err != nil {
			return fmt.Errorf("load migrations fs: %w", err)
		}
		migrator, err := newMigratorForTable(pool, fsys, "schema_migrations")
		if err != nil {
			return fmt.Errorf("new migrator: %w", err)
		}
		if err := migrator.Up(ctx); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		return nil
	})

// TestMain owns shared-container teardown. Runs once per test binary after all
// tests complete.
func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}

// migratedPool returns a Pool to a fresh per-test database cloned from the
// pre-migrated template (production schema present). For tests that need the
// schema and do NOT run their own migrations. Cleanup is registered via
// tb.Cleanup — callers must not Close manually.
func migratedPool(tb testing.TB) *Pool {
	tb.Helper()
	return openPerTestPool(tb, sharedPG.CloneDSN(tb))
}

// emptyPool returns a Pool to a fresh empty per-test database (template1, no
// schema). For migration/schema tests that run their own Up/UpTo/provider.Down
// against a clean database. Cleanup is registered via tb.Cleanup — callers
// must not Close manually.
func emptyPool(tb testing.TB) *Pool {
	tb.Helper()
	return openPerTestPool(tb, sharedPG.EmptyDSN(tb))
}

func openPerTestPool(tb testing.TB, dsn string) *Pool {
	tb.Helper()
	pool, err := NewPool(context.Background(), Config{DSN: dsn})
	if err != nil {
		tb.Fatalf("open per-test pool: %v", err)
	}
	tb.Cleanup(func() {
		if err := pool.Close(context.Background()); err != nil {
			tb.Logf("WARN: per-test pool close: %v", err)
		}
	})
	return pool
}
