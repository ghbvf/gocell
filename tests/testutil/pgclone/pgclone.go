//go:build integration || examples_smoke

// Package pgclone owns the process-wide shared PostgreSQL container lifecycle
// and per-test database minting for integration test packages. It is the
// single sanctioned holder of tcpostgres.Run in the repo (PG-TESTCONTAINER-
// FUNNEL): tests/testutil/pgshare and adapters/postgres's in-package test
// helper both delegate here, so no other package needs to import the
// testcontainers postgres module.
//
// pgclone deliberately does NOT import adapters/postgres. The migration step
// (which needs adapterpg.NewMigrator) is supplied by the caller as a
// MigrateFunc callback. This dependency inversion is what lets
// adapters/postgres's own white-box (`package postgres`) tests share one
// container without an import cycle: pgshare imports adapterpg, so
// adapters/postgres tests cannot import pgshare; they import pgclone, which
// has no adapterpg edge.
//
// Two per-test paths share one container:
//   - CloneDSN: a per-test DB cloned from a pre-migrated template
//     (CREATE DATABASE ... TEMPLATE, file-level copy ~50-200ms). For tests
//     that need the production schema already present.
//   - EmptyDSN: a per-test empty DB (plain CREATE DATABASE = template1). For
//     migration/schema tests that run their own Up/UpTo against a clean DB.
//
// Package-test binaries run in independent processes, so a *Shared is scoped
// to one Go test binary; the value is intra-package sharing — multiple setup
// helpers across multiple _test.go files collapse onto one container + one
// migration.
//
// The PG-TESTCONTAINER-FUNNEL guard (a pure-AST archtest — the callsites are
// //go:build integration files invisible to golangci-lint/typed tooling) keeps
// tcpostgres.Run out of every other package. Its AI-robust rating and
// blind-spot inventory live in tools/archtest/pg_testcontainer_funnel_test.go.
//
// Most callers outside adapters/postgres should use tests/testutil/pgshare,
// which wraps pgclone with a typed *adapterpg.Pool return. Only white-box
// `package postgres` tests (which cannot import pgshare without an import
// cycle) use pgclone directly.
package pgclone

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ghbvf/gocell/tests/testutil"
)

// MigrateFunc applies the caller's migration set to the template database at
// templateDSN. Callers typically use adapterpg.NewPool + NewMigrator + Up with
// a deferred pool.Close.
//
// WARNING: the implementation MUST close every pool/connection it opens before
// returning. CREATE DATABASE ... TEMPLATE rejects a source DB with active
// connections, so a single lingering connection silently breaks EVERY
// subsequent CloneDSN call. Use a deferred Close (see pgshare.applyMigrations
// for the canonical pattern).
type MigrateFunc func(ctx context.Context, templateDSN string) error

// Shared owns the per-package container + template DB lifecycle. Create one
// in a package-scope var and call Shutdown from TestMain.
//
// The zero value is invalid; callers must use New.
type Shared struct {
	templateDB string
	migrate    MigrateFunc

	once     sync.Once
	initErr  error
	adminDSN string
	shutdown func()
}

// New returns a *Shared bound to templateDB and the caller's MigrateFunc.
//
// templateDB must satisfy PostgreSQL identifier rules (see validateTemplateDB):
// non-empty, ≤ 63 chars, starts with [a-z_], only lowercase ascii letters /
// digits / underscores, no NUL or double-quote. Use a unique name per Go test
// binary (`gocell_<pkg>_test_template` is the convention).
//
// Validation + container boot are lazy (first CloneDSN/EmptyDSN call): an
// invalid name surfaces as t.Fatalf there, not a constructor panic. This keeps
// pgclone panic-free (PANIC-REGISTERED-01 forbids unwrapped panic in this
// build-tag-gated non-_test.go file). The instance is dormant until the first
// CloneDSN/EmptyDSN call.
func New(templateDB string, migrate MigrateFunc) *Shared {
	return &Shared{templateDB: templateDB, migrate: migrate}
}

// CloneDSN boots the shared container on first call (one-shot per test
// binary), clones the pre-migrated template into a fresh per-test database,
// and returns a DSN pointing at it. The per-test DB is dropped via t.Cleanup
// (DROP DATABASE ... WITH (FORCE)).
//
// The caller opens its own pool against the returned DSN and registers
// pool.Close via t.Cleanup AFTER this call — t.Cleanup is LIFO, so the pool
// closes before the DB is dropped (and WITH (FORCE) is a belt-and-suspenders
// safeguard if ordering is ever wrong).
func (s *Shared) CloneDSN(tb testing.TB) string {
	tb.Helper()
	return s.perTestDSN(tb, true)
}

// EmptyDSN is like CloneDSN but the per-test database is empty (plain CREATE
// DATABASE = template1, no production schema). For migration/schema tests
// that run their own Up/UpTo against a clean database. The shared container's
// migrated template is still built once on boot (the adopting package uses
// both paths); empty DBs simply skip the clone.
func (s *Shared) EmptyDSN(tb testing.TB) string {
	tb.Helper()
	return s.perTestDSN(tb, false)
}

func (s *Shared) perTestDSN(tb testing.TB, migrated bool) string {
	tb.Helper()
	dsn, drop := s.mint(tb, migrated)
	tb.Cleanup(drop)
	return dsn
}

// CloneManaged is like CloneDSN but returns a manual release func instead of
// registering t.Cleanup(drop). For per-iteration lifecycles — e.g. a benchmark
// that clones inside the b.N loop, where deferring every drop to b.Cleanup
// would accumulate b.N databases until the benchmark ends. The caller MUST
// call release exactly once (typically alongside its own pool.Close).
func (s *Shared) CloneManaged(tb testing.TB) (dsn string, release func()) {
	tb.Helper()
	return s.mint(tb, true)
}

// mint boots the shared container on first call, creates a fresh per-test
// database (cloned from the migrated template when migrated, else empty), and
// returns its DSN plus a drop func. It does NOT register cleanup — perTestDSN
// wires t.Cleanup(drop); CloneManaged hands the drop to the caller.
func (s *Shared) mint(tb testing.TB, migrated bool) (dsn string, drop func()) {
	tb.Helper()
	// Per-call skip-or-fatal gate. RequireDocker either skips (local dev,
	// Docker absent) or fatals (CI with GOCELL_TEST_DOCKER_REQUIRED=1). Called
	// outside sync.Once so the gate fires for every caller even after init ran
	// once — keeps test-binary semantics consistent when Docker disappears
	// mid-suite.
	testutil.RequireDocker(tb)

	s.once.Do(func() { s.boot(tb) })
	if s.initErr != nil {
		tb.Fatalf("shared postgres unavailable: %v", s.initErr)
	}

	ctx := context.Background()
	dbName := newDBName()

	var err error
	if migrated {
		err = cloneDatabase(ctx, s.adminDSN, s.templateDB, dbName)
	} else {
		err = createDatabase(ctx, s.adminDSN, dbName)
	}
	if err != nil {
		tb.Fatalf("mint per-test DB %s (migrated=%v): %v", dbName, migrated, err)
	}

	drop = func() {
		if derr := dropDatabase(context.Background(), s.adminDSN, dbName); derr != nil {
			tb.Logf("WARN: drop per-test DB %s: %v", dbName, derr)
		}
	}
	return swapDatabaseInDSN(s.adminDSN, dbName), drop
}

// Shutdown terminates the shared container if it was booted. Safe to call even
// when the singleton was never initialized (s.shutdown nil → no-op). Call from
// TestMain after m.Run.
func (s *Shared) Shutdown() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

// boot validates the template name, starts the container, creates the template
// DB, applies the caller's migrations, and stores the admin DSN. Runs exactly
// once per *Shared (guarded by sync.Once in perTestDSN).
//
// boot takes testing.TB because the testcontainer archtest
// TestTestcontainerHelpersRequireDockerBeforeRun requires the function that
// calls tcpostgres.Run to also call testutil.RequireDocker first. Errors set
// s.initErr (no t.Fatal inside boot) so the sync.Once gate cannot leave the
// singleton half-initialized — perTestDSN reads s.initErr and surfaces it.
func (s *Shared) boot(tb testing.TB) {
	tb.Helper()

	if err := validateTemplateDB(s.templateDB); err != nil {
		s.initErr = err
		return
	}
	if s.migrate == nil {
		s.initErr = fmt.Errorf("pgclone: migrate func must not be nil")
		return
	}

	// RequireDocker inside the function with tcpostgres.Run — see boot godoc
	// for the archtest rationale.
	testutil.RequireDocker(tb)

	ctx := context.Background()
	// password/user/db are container-internal credentials — the container
	// lives only within this test binary's process and never accepts external
	// traffic; the reused literal across pgclone callers is intentional.
	container, err := tcpostgres.Run(
		ctx, testutil.PostgresImage,
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
		s.terminateAfterInitFailure(ctx, container, fmt.Errorf("admin DSN: %w", err))
		return
	}
	if err := createDatabase(ctx, adminDSN, s.templateDB); err != nil {
		s.terminateAfterInitFailure(ctx, container, fmt.Errorf("create template DB: %w", err))
		return
	}
	templateDSN := swapDatabaseInDSN(adminDSN, s.templateDB)
	if err := s.migrate(ctx, templateDSN); err != nil {
		s.terminateAfterInitFailure(ctx, container, fmt.Errorf("apply migrations to template: %w", err))
		return
	}

	s.adminDSN = adminDSN
	s.shutdown = func() {
		if err := container.Terminate(ctx); err != nil {
			// TestMain runs outside any test context, so *testing.T is
			// unavailable. Emit to slog so CI log scrapers pick up cleanup
			// failures; Ryuk fallback handles the actual reap.
			slog.Default().Error("pgclone: shared container terminate failed",
				"templateDB", s.templateDB, "error", err)
		}
	}
}

// terminateAfterInitFailure tears down the container after a boot-step failure
// and records cause in s.initErr. A terminate failure is logged (Ryuk reaps
// the leak) but does not mask the original cause.
func (s *Shared) terminateAfterInitFailure(ctx context.Context, container *tcpostgres.PostgresContainer, cause error) {
	if termErr := container.Terminate(ctx); termErr != nil {
		slog.Default().Error("pgclone: terminate after init failure also failed",
			"templateDB", s.templateDB, "init_err", cause, "terminate_err", termErr)
	}
	s.initErr = cause
}

// validateTemplateDB returns nil if name is a legal Postgres identifier under
// the rules documented on New.
func validateTemplateDB(templateDB string) error {
	switch {
	case templateDB == "":
		return fmt.Errorf("pgclone: templateDB must not be empty")
	case len(templateDB) > 63:
		return fmt.Errorf("pgclone: templateDB exceeds Postgres NAMEDATALEN limit of 63 chars, got %q", templateDB)
	case strings.ContainsRune(templateDB, '\x00'):
		return fmt.Errorf("pgclone: templateDB must not contain NUL bytes, got %q", templateDB)
	case strings.ContainsRune(templateDB, '"'):
		return fmt.Errorf("pgclone: templateDB must not contain double-quote characters, got %q", templateDB)
	}
	for i, ch := range templateDB {
		if i == 0 {
			if (ch < 'a' || ch > 'z') && ch != '_' {
				return fmt.Errorf("pgclone: templateDB must start with [a-z_], got %q", templateDB)
			}
			continue
		}
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return fmt.Errorf(
				"pgclone: templateDB must contain only lowercase ascii letters, digits, and underscores (no uppercase), got %q",
				templateDB)
		}
	}
	return nil
}

func newDBName() string {
	return "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// execAdmin opens a throwaway admin connection, runs sql, and closes the
// connection before return. Closing matters for CREATE DATABASE ... TEMPLATE:
// Postgres rejects cloning a source DB that has active connections, so a
// lingering admin connection here would block subsequent clones.
func execAdmin(ctx context.Context, adminDSN, sql string) error {
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer func() {
		if cerr := conn.Close(ctx); cerr != nil {
			slog.Default().Warn("pgclone: admin conn close failed", "error", cerr)
		}
	}()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("exec admin sql: %w", err)
	}
	return nil
}

// createDatabase creates an empty database (template1 default). Used both for
// the shared template DB (boot, before migration) and for EmptyDSN per-test
// databases.
func createDatabase(ctx context.Context, adminDSN, name string) error {
	return execAdmin(ctx, adminDSN, "CREATE DATABASE "+pgQuoteIdent(name))
}

// cloneDatabase creates newDB as a file-level copy of templateDB.
func cloneDatabase(ctx context.Context, adminDSN, templateDB, newDB string) error {
	return execAdmin(ctx, adminDSN, fmt.Sprintf(
		"CREATE DATABASE %s TEMPLATE %s", pgQuoteIdent(newDB), pgQuoteIdent(templateDB)))
}

// dropDatabase removes a per-test database via WITH (FORCE) to terminate any
// straggling connections (the per-test pool should already be Closed; FORCE is
// a belt-and-suspenders safeguard against test cleanup ordering bugs).
func dropDatabase(ctx context.Context, adminDSN, name string) error {
	return execAdmin(ctx, adminDSN, "DROP DATABASE IF EXISTS "+pgQuoteIdent(name)+" WITH (FORCE)")
}

// swapDatabaseInDSN rewrites the database segment of a postgres DSN.
// testcontainers-go modules/postgres always emits the canonical form
// `scheme://user:pass@host:port/<db>?<query>` so a last-`/` + first-`?` split
// is sufficient.
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
