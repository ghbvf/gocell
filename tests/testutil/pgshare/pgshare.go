//go:build integration

// Package pgshare provides a process-wide shared PostgreSQL container with
// pre-migrated template-DB clone semantics for integration test packages.
//
// Each package that needs an isolated per-test PostgreSQL database creates
// one *Shared instance (typically in TestMain) bound to a unique template
// database name. The first NewPerTestPool call boots the container and
// applies migrations once into the template; subsequent calls clone the
// template via `CREATE DATABASE ... TEMPLATE <template>` — file-level
// copy at ~50-200ms, dramatically cheaper than the ~7-10s container cold
// start the historic per-test setup helpers paid.
//
// Package-test binaries run in independent processes, so the *Shared
// instance is scoped to one Go test binary. Sharing across packages is
// not possible (Go's test runner forbids cross-binary state) — the value
// is intra-package: multiple setup helpers across multiple _test.go files
// in the same package collapse onto one container + one migration.
//
// Mirrors the pattern in adapters/rabbitmq/testmain_integration_test.go
// (lazy singleton + TestMain teardown), generalized for PG's TEMPLATE
// clone capability.
package pgshare

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/tests/testutil"
)

// Shared owns the per-package container + template DB lifecycle. Create
// one in a package-scope `var` and call Shutdown from TestMain.
//
// The zero value is invalid; callers must use New.
type Shared struct {
	templateDB string

	once     sync.Once
	initErr  error
	adminDSN string
	shutdown func()
}

// New returns a *Shared bound to the given template database name. Names
// must satisfy PostgreSQL identifier rules (lowercase + underscores
// recommended, ≤63 chars). The instance is dormant until the first
// NewPerTestPool call.
//
// Use a unique name per Go test binary (`gocell_<pkg>_test_template` is
// the convention) so a future caller that opens multiple Shared instances
// in one process cannot accidentally collide.
func New(templateDB string) *Shared {
	return &Shared{templateDB: templateDB}
}

// NewPerTestPool clones the shared template database into a fresh
// per-test database, opens a Pool against it, and registers cleanup via
// t.Cleanup. The returned pool's lifetime is bound to the test; callers
// must NOT call pool.Close manually.
//
// First call also boots the container and applies migrations (one-shot
// for the test binary). Subsequent calls pay only the file-clone cost.
func (s *Shared) NewPerTestPool(t *testing.T) *adapterpg.Pool {
	t.Helper()
	testutil.RequireDocker(t)

	s.once.Do(s.init)
	if s.initErr != nil {
		t.Fatalf("shared postgres unavailable: %v", s.initErr)
	}

	ctx := context.Background()
	dbName := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	if err := cloneTemplateDB(ctx, s.adminDSN, s.templateDB, dbName); err != nil {
		t.Fatalf("clone template DB: %v", err)
	}

	perTestDSN := swapDatabaseInDSN(s.adminDSN, dbName)
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: perTestDSN})
	if err != nil {
		t.Fatalf("open pool to per-test DB %s: %v", dbName, err)
	}

	t.Cleanup(func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: per-test pool close (%s): %v", dbName, err)
		}
		if err := dropDB(ctx, s.adminDSN, dbName); err != nil {
			t.Logf("WARN: drop per-test DB %s: %v", dbName, err)
		}
	})

	return pool
}

// Shutdown terminates the shared container if it was booted. Safe to call
// even when the singleton was never initialized; mirrors
// rabbitmq.testmain_integration_test.go pattern.
func (s *Shared) Shutdown() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

func (s *Shared) init() {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		s.initErr = fmt.Errorf("migrations fs: %w", err)
		return
	}
	s.initWithMigrationsFS(migrationsFS)
}

// initWithMigrationsFS is split out for clarity; the public API always
// uses the adapter's embedded migrations FS (the only migration source
// of truth for PG integration tests today).
func (s *Shared) initWithMigrationsFS(migrationsFS fs.FS) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		s.initErr = fmt.Errorf("start shared postgres: %w", err)
		return
	}
	adminDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		s.initErr = fmt.Errorf("admin DSN: %w", err)
		return
	}

	if err := createTemplateDB(ctx, adminDSN, s.templateDB); err != nil {
		_ = container.Terminate(ctx)
		s.initErr = fmt.Errorf("create template DB: %w", err)
		return
	}

	templateDSN := swapDatabaseInDSN(adminDSN, s.templateDB)
	if err := applyMigrationsToTemplate(ctx, templateDSN, migrationsFS); err != nil {
		_ = container.Terminate(ctx)
		s.initErr = fmt.Errorf("apply migrations to template: %w", err)
		return
	}

	s.adminDSN = adminDSN
	s.shutdown = func() {
		if err := container.Terminate(ctx); err != nil {
			// TestMain runs outside any test context, so *testing.T is
			// unavailable. Emit to slog so CI log scrapers pick up
			// cleanup failures; Ryuk fallback handles the actual reap.
			slog.Default().Warn("pgshare: shared container terminate failed",
				"templateDB", s.templateDB, "error", err)
		}
	}
}

// createTemplateDB opens a throwaway admin connection, issues CREATE
// DATABASE, and closes the connection before return — Postgres rejects
// CREATE DATABASE ... TEMPLATE <db> while <db> has any active connections,
// so leaving the admin connection open here would block the migration
// step that follows.
func createTemplateDB(ctx context.Context, adminDSN, templateDB string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgQuoteIdent(templateDB)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

// applyMigrationsToTemplate opens a dedicated pool to the template DB,
// runs the full migration set, and closes the pool. The pool MUST be
// closed before return: per the CREATE DATABASE TEMPLATE constraint
// (see createTemplateDB), any lingering connection on the source DB
// blocks subsequent clones.
func applyMigrationsToTemplate(ctx context.Context, templateDSN string, migrationsFS fs.FS) error {
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: templateDSN})
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer func() {
		_ = pool.Close(ctx)
	}()

	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, "schema_migrations")
	if err != nil {
		return fmt.Errorf("new migrator: %w", err)
	}
	if err := migrator.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func cloneTemplateDB(ctx context.Context, adminDSN, templateDB, newDB string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	q := fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s",
		pgQuoteIdent(newDB), pgQuoteIdent(templateDB))
	if _, err := conn.Exec(ctx, q); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	return nil
}

// dropDB removes a per-test database via WITH (FORCE) to terminate any
// straggling connections (the per-test pool should already be Closed at
// this point; FORCE is a belt-and-suspenders safeguard against test
// cleanup ordering bugs).
func dropDB(ctx context.Context, adminDSN, dbName string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	q := "DROP DATABASE IF EXISTS " + pgQuoteIdent(dbName) + " WITH (FORCE)"
	if _, err := conn.Exec(ctx, q); err != nil {
		return fmt.Errorf("drop: %w", err)
	}
	return nil
}

// swapDatabaseInDSN rewrites the database segment of a postgres DSN.
// testcontainers-go modules/postgres always emits the canonical form
// `scheme://user:pass@host:port/<db>?<query>` so a last-`/` + first-`?`
// split is sufficient.
func swapDatabaseInDSN(dsn, newDB string) string {
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		return dsn
	}
	before := dsn[:idx+1]
	rest := dsn[idx+1:]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		return before + newDB + rest[q:]
	}
	return before + newDB
}

func pgQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
