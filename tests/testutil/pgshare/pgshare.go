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

// New returns a *Shared bound to the given template database name. The
// name must satisfy PostgreSQL identifier rules:
//   - non-empty
//   - ≤ 63 characters (Postgres NAMEDATALEN limit)
//   - starts with [a-z_] (no uppercase, no digit-prefix)
//   - contains only lowercase ASCII letters, digits, and underscores
//   - must not contain NUL bytes (\x00) or double-quote characters (")
//
// Validation runs at first NewPerTestPool call (lazy): an invalid name
// produces a `t.Fatalf` rather than panicking at binary start. This keeps
// pgshare panic-free (PANIC-REGISTERED-01 archtest forbids unwrapped
// panic in non-test production code, and pgshare.go is build-tag-gated
// but not a `_test.go` file).
//
// Use a unique name per Go test binary (`gocell_<pkg>_test_template` is
// the convention) so a future caller that opens multiple Shared instances
// in one process cannot accidentally collide.
//
// The instance is dormant until the first NewPerTestPool call.
func New(templateDB string) *Shared {
	return &Shared{templateDB: templateDB}
}

// validateTemplateDB returns nil if the name is a legal Postgres identifier
// under the rules documented on New. Called from init() so violations
// surface as a `t.Fatalf` at first NewPerTestPool — see New godoc for
// why this is lazy and not enforced in the constructor.
func validateTemplateDB(templateDB string) error {
	switch {
	case templateDB == "":
		return fmt.Errorf("pgshare: templateDB must not be empty")
	case len(templateDB) > 63:
		return fmt.Errorf("pgshare: templateDB exceeds Postgres NAMEDATALEN limit of 63 chars, got %q", templateDB)
	case strings.ContainsRune(templateDB, '\x00'):
		return fmt.Errorf("pgshare: templateDB must not contain NUL bytes, got %q", templateDB)
	case strings.ContainsRune(templateDB, '"'):
		return fmt.Errorf("pgshare: templateDB must not contain double-quote characters, got %q", templateDB)
	}
	for i, ch := range templateDB {
		if i == 0 {
			if !((ch >= 'a' && ch <= 'z') || ch == '_') {
				return fmt.Errorf("pgshare: templateDB must start with [a-z_], got %q", templateDB)
			}
			continue
		}
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') {
			return fmt.Errorf("pgshare: templateDB must contain only lowercase ascii letters, digits, and underscores (no uppercase), got %q", templateDB)
		}
	}
	return nil
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
	// Per-call skip-or-fatal gate. RequireDocker either skips (local dev,
	// Docker absent) or fatals (CI with GOCELL_TEST_DOCKER_REQUIRED=1).
	// Called outside sync.Once so the gate fires for every caller even
	// after init has run once — keeps test-binary semantics consistent
	// when Docker disappears mid-suite.
	testutil.RequireDocker(t)

	// sync.Once cannot accept arguments; wrap to plumb t into boot so the
	// archtest TestTestcontainerHelpersRequireDockerBeforeRun (function
	// containing tcpostgres.Run must call RequireDocker first) is satisfied
	// by boot itself, not solely by the outer NewPerTestPool. The wrapper
	// closure captures the first caller's t — failure paths set s.initErr
	// (no t.Fatal inside boot) so subsequent callers observe the same error
	// via the read below.
	s.once.Do(func() { s.boot(t) })
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
// If init() failed (Docker unavailable, migration error, etc.), s.shutdown
// remains nil and this method is a no-op — there is no container to terminate.
func (s *Shared) Shutdown() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

// boot validates the template name, starts the container, applies
// migrations to the template DB, and stores the admin DSN. Runs exactly
// once per *Shared (guarded by sync.Once in NewPerTestPool).
//
// boot takes *testing.T because the testcontainer archtest
// TestTestcontainerHelpersRequireDockerBeforeRun requires the function
// that calls tcpostgres.Run to also call testutil.RequireDocker first
// (same-function check). The outer NewPerTestPool already calls
// RequireDocker for the per-call skip gate; the second call here is
// mildly redundant but satisfies the archtest and is defensive if
// Docker disappears between the two checks.
//
// Errors set s.initErr; no t.Fatal is invoked inside boot so that the
// sync.Once gate cannot leave the singleton in a half-initialized
// state — the outer NewPerTestPool reads s.initErr and surfaces it.
func (s *Shared) boot(t *testing.T) {
	t.Helper()

	if err := validateTemplateDB(s.templateDB); err != nil {
		s.initErr = err
		return
	}
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		s.initErr = fmt.Errorf("migrations fs: %w", err)
		return
	}

	// RequireDocker inside the function with tcpostgres.Run — see boot
	// godoc for the archtest rationale.
	testutil.RequireDocker(t)

	ctx := context.Background()
	// password/user/db are container-internal credentials — the container
	// lives only within this test binary's process and never accepts external
	// traffic; reused literal across pgshare-using packages is intentional.
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
		if termErr := container.Terminate(ctx); termErr != nil {
			slog.Default().Error("pgshare: terminate after init failure also failed",
				"templateDB", s.templateDB, "init_err", err, "terminate_err", termErr)
		}
		s.initErr = fmt.Errorf("admin DSN: %w", err)
		return
	}

	if err := createTemplateDB(ctx, adminDSN, s.templateDB); err != nil {
		if termErr := container.Terminate(ctx); termErr != nil {
			slog.Default().Error("pgshare: terminate after init failure also failed",
				"templateDB", s.templateDB, "init_err", err, "terminate_err", termErr)
		}
		s.initErr = fmt.Errorf("create template DB: %w", err)
		return
	}

	templateDSN := swapDatabaseInDSN(adminDSN, s.templateDB)
	if err := applyMigrationsToTemplate(ctx, templateDSN, migrationsFS); err != nil {
		if termErr := container.Terminate(ctx); termErr != nil {
			slog.Default().Error("pgshare: terminate after init failure also failed",
				"templateDB", s.templateDB, "init_err", err, "terminate_err", termErr)
		}
		s.initErr = fmt.Errorf("apply migrations to template: %w", err)
		return
	}

	s.adminDSN = adminDSN
	s.shutdown = func() {
		if err := container.Terminate(ctx); err != nil {
			// TestMain runs outside any test context, so *testing.T is
			// unavailable. Emit to slog so CI log scrapers pick up
			// cleanup failures; Ryuk fallback handles the actual reap.
			// Error level: container leak impacts ops; Ryuk fallback
			// eventually reaps, but operators should see the failure
			// during normal runs.
			slog.Default().Error("pgshare: shared container terminate failed",
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
