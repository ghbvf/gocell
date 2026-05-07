//go:build integration

package postgres

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tests/testutil"
)

// ---------------------------------------------------------------------------
// Shared base container (one per test binary run)
//
// B1 fix: a single PG container is started via sync.Once so all top-level
// tests in this package share one container launch rather than each spinning
// up their own. Per-test isolation is achieved by assigning each test a unique
// PG schema (setupPGPool). Container teardown is handled by testcontainers'
// Ryuk reaper on process exit.
// ---------------------------------------------------------------------------

var (
	sharedBaseOnce    sync.Once
	sharedBaseConnStr string
)

// acquireSharedConnStr returns the connection string for the single shared PG
// container, starting it exactly once per test binary run.
func acquireSharedConnStr(t *testing.T) string {
	t.Helper()
	testutil.RequireDocker(t)

	var startErr error
	sharedBaseOnce.Do(func() {
		ctx := context.Background()
		container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
			tcpostgres.WithDatabase("test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			startErr = err
			return
		}
		connStr, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = container.Terminate(ctx)
			startErr = err
			return
		}
		sharedBaseConnStr = connStr
		// Container lifecycle is managed by testcontainers' Ryuk reaper;
		// explicit Terminate is not required for test correctness.
	})

	if startErr != nil {
		t.Fatalf("acquireSharedConnStr: start postgres container: %v", startErr)
	}

	return sharedBaseConnStr
}

// setupPGPool creates an isolated-schema pool for a single test.
//
// It uses the shared PG container (started once via acquireSharedConnStr),
// creates a unique schema, runs all migrations inside it, and registers
// t.Cleanup to drop the schema and close the pool when the test ends.
//
// The three repository setup helpers (setupUserRepoPG / setupSessionRepoPG /
// setupRoleRepoPG) delegate container+migration bootstrap here; they only
// construct the repository on top of the returned pool.
func setupPGPool(t *testing.T) *adapterpg.Pool {
	t.Helper()

	baseConnStr := acquireSharedConnStr(t)
	ctx := context.Background()

	// Use a short-lived bootstrap pool to create the isolated schema.
	schema := fmt.Sprintf("ac%016x", rand.Int63())
	bootstrap, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: baseConnStr})
	if err != nil {
		t.Fatalf("setupPGPool: bootstrap pool: %v", err)
	}
	if _, err := bootstrap.DB().Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = bootstrap.Close(ctx)
		t.Fatalf("setupPGPool: create schema %s: %v", schema, err)
	}
	_ = bootstrap.Close(ctx)

	// Build a DSN that pins search_path to the new schema.
	// pgx v5 accepts search_path as a URL query parameter appended to the base DSN.
	schemaDSN := baseConnStr + fmt.Sprintf("&search_path=%s", schema)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: schemaDSN})
	if err != nil {
		t.Fatalf("setupPGPool: schema pool: %v", err)
	}

	fsys, err := adapterpg.MigrationsFS()
	if err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("setupPGPool: get migrations FS: %v", err)
	}

	migrator, err := adapterpg.NewMigrator(pool, fsys, "schema_migrations")
	if err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("setupPGPool: create migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("setupPGPool: run migrations: %v", err)
	}

	t.Cleanup(func() {
		_ = pool.Close(ctx)
		// Drop the isolated schema; errors are non-fatal (Ryuk cleans up the
		// container anyway, but eager cleanup keeps shared DB space lean).
		dropConn, derr := adapterpg.NewPool(context.Background(), adapterpg.Config{DSN: baseConnStr})
		if derr == nil {
			_, _ = dropConn.DB().Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			_ = dropConn.Close(context.Background())
		}
	})

	return pool
}
