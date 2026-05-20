//go:build integration

package pgshare

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestValidateTemplateDB exhaustively exercises each rejection branch of
// validateTemplateDB plus the happy path. Validation is the only pgshare
// surface that's reachable without Docker — every other function path
// either connects to PG or calls testcontainers, and is covered by the
// adopting packages' integration tests (configcore/accesscore PG repos,
// configwrite/configpublish slices).
func TestValidateTemplateDB(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantErr   bool
		wantMatch string // substring of the error message
	}{
		{name: "valid_lowercase", input: "gocell_test_template", wantErr: false},
		{name: "valid_underscore_prefix", input: "_template", wantErr: false},
		{name: "valid_with_digits", input: "test_2025", wantErr: false},
		{name: "valid_single_letter", input: "x", wantErr: false},
		{name: "valid_max_length", input: strings.Repeat("a", 63), wantErr: false},

		{name: "empty", input: "", wantErr: true, wantMatch: "must not be empty"},
		{name: "too_long", input: strings.Repeat("a", 64), wantErr: true, wantMatch: "NAMEDATALEN"},
		{name: "contains_null_byte", input: "test\x00drop", wantErr: true, wantMatch: "NUL bytes"},
		{name: "contains_double_quote", input: `test"db`, wantErr: true, wantMatch: "double-quote"},
		{name: "digit_prefix", input: "1test", wantErr: true, wantMatch: "must start with [a-z_]"},
		{name: "uppercase_prefix", input: "Test", wantErr: true, wantMatch: "must start with [a-z_]"},
		{name: "uppercase_middle", input: "testDB", wantErr: true, wantMatch: "only lowercase ascii"},
		{name: "hyphen_invalid", input: "test-db", wantErr: true, wantMatch: "only lowercase ascii"},
		{name: "dot_invalid", input: "test.db", wantErr: true, wantMatch: "only lowercase ascii"},
		{name: "space_invalid", input: "test db", wantErr: true, wantMatch: "only lowercase ascii"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTemplateDB(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateTemplateDB(%q): want error, got nil", tc.input)
				}
				if !strings.Contains(err.Error(), tc.wantMatch) {
					t.Fatalf("validateTemplateDB(%q): error %q must contain %q",
						tc.input, err.Error(), tc.wantMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateTemplateDB(%q): want nil, got %v", tc.input, err)
			}
		})
	}
}

// TestSwapDatabaseInDSN covers every branch of the DSN rewrite helper:
// canonical testcontainers form (with query), no-query form, malformed
// input without `/`, and edge cases around path/query boundaries.
func TestSwapDatabaseInDSN(t *testing.T) {
	cases := []struct {
		name  string
		dsn   string
		newDB string
		want  string
	}{
		{
			name:  "canonical_with_query",
			dsn:   "postgres://test:test@localhost:5432/test?sslmode=disable",
			newDB: "fresh",
			want:  "postgres://test:test@localhost:5432/fresh?sslmode=disable",
		},
		{
			name:  "no_query",
			dsn:   "postgres://u:p@h:5432/admin",
			newDB: "newdb",
			want:  "postgres://u:p@h:5432/newdb",
		},
		{
			name:  "complex_query_preserved",
			dsn:   "postgres://u:p@h/old?sslmode=disable&pool_max_conns=10",
			newDB: "isolated_42",
			want:  "postgres://u:p@h/isolated_42?sslmode=disable&pool_max_conns=10",
		},
		{
			name:  "no_slash_returns_input",
			dsn:   "not-a-dsn",
			newDB: "ignored",
			want:  "not-a-dsn",
		},
		{
			name:  "trailing_slash_only",
			dsn:   "postgres://h/",
			newDB: "x",
			want:  "postgres://h/x",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := swapDatabaseInDSN(tc.dsn, tc.newDB)
			if got != tc.want {
				t.Fatalf("swapDatabaseInDSN(%q, %q) = %q, want %q",
					tc.dsn, tc.newDB, got, tc.want)
			}
		})
	}
}

// TestPgQuoteIdent verifies identifier double-quoting: plain identifiers
// are wrapped in `"`, and embedded `"` characters are escaped by doubling.
// This is the SQL-injection defense for caller-supplied templateDB names
// (the canonical pgQuoteIdent contract per PostgreSQL docs).
func TestPgQuoteIdent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "mydb", want: `"mydb"`},
		{name: "empty", in: "", want: `""`},
		{name: "contains_double_quote", in: `bad"name`, want: `"bad""name"`},
		{name: "multiple_quotes", in: `"a"b"`, want: `"""a""b"""`},
		{name: "uppercase_preserved", in: "MyDB", want: `"MyDB"`},
		{name: "whitespace_preserved", in: "my db", want: `"my db"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pgQuoteIdent(tc.in)
			if got != tc.want {
				t.Fatalf("pgQuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNew_StoresTemplateDBVerbatim asserts that New is a thin
// constructor — it must NOT validate or transform templateDB (validation
// is lazy, runs at first init() — see New godoc for the rationale).
// Storing a name that would later fail validation must succeed here;
// the error only surfaces from NewPerTestPool via t.Fatalf.
func TestNew_StoresTemplateDBVerbatim(t *testing.T) {
	t.Parallel()
	// A name that validateTemplateDB rejects; New must still return a
	// non-nil *Shared with the bad name stored verbatim.
	bad := "1invalid-name"
	s := New(bad)
	if s == nil {
		t.Fatal("New returned nil for syntactically bad templateDB; expected non-nil with lazy validation")
	}
	if s.templateDB != bad {
		t.Fatalf("templateDB stored as %q, want verbatim %q", s.templateDB, bad)
	}
}

// TestShared_Shutdown_NoOpWhenUninitialized asserts the no-op promise
// documented on Shutdown: calling Shutdown before any NewPerTestPool
// (i.e. when init never ran, so s.shutdown is nil) must not panic and
// must not touch the shared singleton.
func TestShared_Shutdown_NoOpWhenUninitialized(t *testing.T) {
	t.Parallel()
	s := New("never_initialized")
	// Must not panic.
	s.Shutdown()
	// init still hasn't run.
	if s.shutdown != nil {
		t.Fatalf("Shutdown on uninitialized *Shared must not populate shutdown closure; got non-nil")
	}
	if s.initErr != nil {
		t.Fatalf("Shutdown must not trigger init; got initErr = %v", s.initErr)
	}
}

// TestShared_init_RejectsInvalidTemplateDB asserts that init() catches
// invalid template names and surfaces them via s.initErr — the lazy
// validation path that replaced the constructor panic. Verifies the
// PANIC-REGISTERED-01 refactor preserves fail-loud behavior.
func TestShared_init_RejectsInvalidTemplateDB(t *testing.T) {
	t.Parallel()
	s := New("Bad_Name") // uppercase → rejected by validateTemplateDB
	// Call init directly (bypass NewPerTestPool's Docker dependency).
	s.init()
	if s.initErr == nil {
		t.Fatal("init() must populate initErr for invalid templateDB")
	}
	if !strings.Contains(s.initErr.Error(), "must start with") {
		t.Fatalf("initErr %q must reference the validation rule", s.initErr.Error())
	}
	// Subsequent inits via sync.Once would also short-circuit — verify
	// the once gate is consistent with the manual call above.
	s2 := New("Bad_Name")
	s2.once.Do(s2.init)
	if s2.initErr == nil {
		t.Fatal("init() through sync.Once must populate initErr")
	}
}

// TestShared_zeroValueIsInvalid pins the godoc-stated contract that the
// zero value is not usable. Calling init() with an empty templateDB
// (the zero-value field) must produce a validation error rather than
// silently proceeding to Docker.
func TestShared_zeroValueIsInvalid(t *testing.T) {
	t.Parallel()
	var s Shared // zero value
	s.init()
	if s.initErr == nil {
		t.Fatal("zero-value *Shared must yield validation error on init()")
	}
	if !strings.Contains(s.initErr.Error(), "must not be empty") {
		t.Fatalf("zero-value initErr %q must mention empty-name rule", s.initErr.Error())
	}
}

// Package-level shared instance used by the Docker-bound integration
// tests below. A single container serves the whole pgshare self-test
// suite — the same pattern the package exposes to its callers.
var pgshareSelfTest = New("gocell_pgshare_selftest_template")

// TestMain owns shared-container teardown for the Docker-bound tests
// below. Runs once per test binary after all tests complete.
func TestMain(m *testing.M) {
	code := m.Run()
	pgshareSelfTest.Shutdown()
	os.Exit(code)
}

// TestNewPerTestPool_IsolatesPerTestDatabases is the headline integration
// test: it boots the shared container (first call), clones two per-test
// databases from the template, and asserts the two databases are fully
// isolated — a write into one is invisible from the other. This exercises
// the full Docker path: tcpostgres.Run → createTemplateDB →
// applyMigrationsToTemplate → cloneTemplateDB → adapterpg.NewPool.
//
// The two subtests share one container via the package-level
// pgshareSelfTest singleton; cleanup of each per-test DB is owned by
// t.Cleanup inside NewPerTestPool, not by these test bodies.
func TestNewPerTestPool_IsolatesPerTestDatabases(t *testing.T) {
	poolA := pgshareSelfTest.NewPerTestPool(t)
	poolB := pgshareSelfTest.NewPerTestPool(t)

	ctx := context.Background()

	// Both pools must be alive and pointing at distinct databases.
	// Use a transient throwaway table since the template migration set
	// is the production adapters/postgres schema — we don't want to
	// depend on schema details here, just on per-DB isolation.
	tableA := "pgshare_iso_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := poolA.DB().Exec(ctx, "CREATE TABLE "+tableA+" (x int)"); err != nil {
		t.Fatalf("create table in pool A: %v", err)
	}
	if _, err := poolA.DB().Exec(ctx, "INSERT INTO "+tableA+" (x) VALUES (1)"); err != nil {
		t.Fatalf("insert into pool A: %v", err)
	}

	// Pool B must NOT see the table — different database.
	var exists bool
	row := poolB.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		tableA)
	if err := row.Scan(&exists); err != nil {
		t.Fatalf("isolation probe on pool B: %v", err)
	}
	if exists {
		t.Fatalf("isolation broken: table %s created in pool A is visible from pool B", tableA)
	}

	// Pool A still sees the row — its own DB persisted across the probe.
	var count int
	if err := poolA.DB().QueryRow(ctx, "SELECT COUNT(*) FROM "+tableA).Scan(&count); err != nil {
		t.Fatalf("recount on pool A: %v", err)
	}
	if count != 1 {
		t.Fatalf("pool A lost its row: count = %d, want 1", count)
	}
}

// TestNewPerTestPool_TemplateSchemaApplied verifies that the migrations
// embedded in adapters/postgres are applied to the template DB and
// inherited by every clone — a clone should already have the
// schema_migrations row set and the production tables present. If the
// template-init path silently skipped migrations, this would fail.
func TestNewPerTestPool_TemplateSchemaApplied(t *testing.T) {
	pool := pgshareSelfTest.NewPerTestPool(t)
	ctx := context.Background()

	// schema_migrations is the goose/migrator bookkeeping table populated
	// by applyMigrationsToTemplate. Any cloned DB inherits it.
	var migrationCount int
	row := pool.DB().QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations")
	if err := row.Scan(&migrationCount); err != nil {
		t.Fatalf("schema_migrations probe: %v — migrations may not have been applied to template", err)
	}
	if migrationCount == 0 {
		t.Fatal("schema_migrations is empty in cloned DB; template did not receive migrations")
	}
}

// TestSharedSingleton_ContainerStartsOnce verifies that two
// NewPerTestPool calls share the same container — observable via
// adminDSN being non-empty after first call and stable across subsequent
// calls. (Direct container-id check would couple to testcontainers
// internals; adminDSN is the public observable that proves singleton
// reuse.)
func TestSharedSingleton_ContainerStartsOnce(t *testing.T) {
	_ = pgshareSelfTest.NewPerTestPool(t)
	dsn1 := pgshareSelfTest.adminDSN
	if dsn1 == "" {
		t.Fatal("adminDSN empty after first NewPerTestPool; init did not populate")
	}

	_ = pgshareSelfTest.NewPerTestPool(t)
	dsn2 := pgshareSelfTest.adminDSN
	if dsn1 != dsn2 {
		t.Fatalf("adminDSN changed across calls; container was re-spawned (dsn1=%q dsn2=%q)",
			dsn1, dsn2)
	}
}
