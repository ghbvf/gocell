//go:build integration

// Package testfx provides exported integration-test helpers for packages that
// need a real PG pool but cannot import the internal _test package.
//
// Typical consumer: cells/accesscore/slices/sessionrefresh (package sessionrefresh)
// needs a pool+migrations setup identical to the one used by
// cells/accesscore/internal/adapters/postgres integration tests.
//
// Design: a single PG container is started once per test binary run via
// sync.Once (shared base). Each caller gets an isolated schema so tests are
// fully independent. Container teardown is handled by testcontainers' Ryuk
// reaper on process exit.
package testfx

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

var (
	sharedOnce    sync.Once
	sharedConnStr string
)

// acquireConnStr returns the base connection string for the shared PG container,
// starting it exactly once per test binary run.
func acquireConnStr(t *testing.T) string {
	t.Helper()
	testutil.RequireDocker(t)

	var startErr error
	sharedOnce.Do(func() {
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
		sharedConnStr = connStr
		// Container lifecycle is managed by testcontainers' Ryuk reaper;
		// explicit Terminate is not required for test correctness.
	})

	if startErr != nil {
		t.Fatalf("testfx.acquireConnStr: start postgres container: %v", startErr)
	}
	return sharedConnStr
}

// SetupPGPool creates an isolated-schema pool with all migrations applied.
//
// The returned pool is scoped to a unique PG schema and is closed automatically
// via t.Cleanup. Callers must NOT close it manually.
//
// This is the exported equivalent of the internal setupPGPool helper used by
// cells/accesscore/internal/adapters/postgres integration tests.
func SetupPGPool(t *testing.T) *adapterpg.Pool {
	t.Helper()

	baseConnStr := acquireConnStr(t)
	ctx := context.Background()

	// Create isolated schema via a short-lived bootstrap pool.
	schema := fmt.Sprintf("fx%016x", rand.Int63())
	bootstrap, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: baseConnStr})
	if err != nil {
		t.Fatalf("testfx.SetupPGPool: bootstrap pool: %v", err)
	}
	if _, err := bootstrap.DB().Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = bootstrap.Close(ctx)
		t.Fatalf("testfx.SetupPGPool: create schema %s: %v", schema, err)
	}
	_ = bootstrap.Close(ctx)

	// Build a DSN that pins search_path to the new schema.
	// pgx v5 accepts search_path as a URL query parameter appended to the base DSN.
	schemaDSN := baseConnStr + fmt.Sprintf("&search_path=%s", schema)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: schemaDSN})
	if err != nil {
		t.Fatalf("testfx.SetupPGPool: schema pool: %v", err)
	}

	fsys, err := adapterpg.MigrationsFS()
	if err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("testfx.SetupPGPool: get migrations FS: %v", err)
	}

	migrator, err := adapterpg.NewMigrator(pool, fsys, "schema_migrations")
	if err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("testfx.SetupPGPool: create migrator: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		_ = pool.Close(ctx)
		t.Fatalf("testfx.SetupPGPool: run migrations: %v", err)
	}

	t.Cleanup(func() {
		_ = pool.Close(ctx)
		dropConn, derr := adapterpg.NewPool(context.Background(), adapterpg.Config{DSN: baseConnStr})
		if derr == nil {
			_, _ = dropConn.DB().Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			_ = dropConn.Close(context.Background())
		}
	})

	return pool
}
