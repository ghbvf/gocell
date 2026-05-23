//go:build integration

package pgclone

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TestValidateTemplateDB exhaustively exercises each rejection branch of
// validateTemplateDB plus the happy path. Validation is the only pgclone
// surface reachable without Docker.
func TestValidateTemplateDB(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantErr   bool
		wantMatch string
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

// TestSwapDatabaseInDSN covers every branch of the DSN rewrite helper.
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

// TestPgQuoteIdent verifies identifier double-quoting (SQL-injection defense
// for caller-supplied templateDB names).
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

// TestNew_StoresFieldsVerbatim asserts New is a thin constructor — it must NOT
// validate or transform templateDB (validation is lazy at first perTestDSN).
func TestNew_StoresFieldsVerbatim(t *testing.T) {
	t.Parallel()
	bad := "1invalid-name"
	s := New(bad, nil)
	if s == nil {
		t.Fatal("New returned nil for syntactically bad templateDB; expected non-nil with lazy validation")
	}
	if s.templateDB != bad {
		t.Fatalf("templateDB stored as %q, want verbatim %q", s.templateDB, bad)
	}
}

// TestShared_Shutdown_NoOpWhenUninitialized asserts Shutdown before any
// perTestDSN call (s.shutdown nil) does not panic and does not touch state.
func TestShared_Shutdown_NoOpWhenUninitialized(t *testing.T) {
	t.Parallel()
	s := New("never_initialized", nil)
	s.Shutdown()
	if s.shutdown != nil {
		t.Fatalf("Shutdown on uninitialized *Shared must not populate shutdown closure; got non-nil")
	}
	if s.initErr != nil {
		t.Fatalf("Shutdown must not trigger init; got initErr = %v", s.initErr)
	}
}

// TestShared_boot_RejectsInvalidTemplateDB asserts boot catches invalid names
// at the validation gate (before RequireDocker / migrate), so this does NOT
// require Docker. nil migrate is fine — validation short-circuits first.
func TestShared_boot_RejectsInvalidTemplateDB(t *testing.T) {
	t.Parallel()
	s := New("Bad_Name", nil) // uppercase → rejected by validateTemplateDB
	s.boot(t)
	if s.initErr == nil {
		t.Fatal("boot() must populate initErr for invalid templateDB")
	}
	if !strings.Contains(s.initErr.Error(), "must start with") {
		t.Fatalf("initErr %q must reference the validation rule", s.initErr.Error())
	}
}

// TestShared_zeroValueIsInvalid pins the godoc-stated contract that the zero
// value is not usable: boot with empty templateDB errors before RequireDocker.
func TestShared_zeroValueIsInvalid(t *testing.T) {
	t.Parallel()
	var s Shared // zero value
	s.boot(t)
	if s.initErr == nil {
		t.Fatal("zero-value *Shared must yield validation error on boot()")
	}
	if !strings.Contains(s.initErr.Error(), "must not be empty") {
		t.Fatalf("zero-value initErr %q must mention empty-name rule", s.initErr.Error())
	}
}

// TestShared_boot_RejectsNilMigrate pins the fail-fast for a nil MigrateFunc:
// boot must surface it via initErr (after name validation, before RequireDocker
// / container start) rather than panicking when it later calls s.migrate. The
// template name is valid so validation passes and the nil check is reached.
func TestShared_boot_RejectsNilMigrate(t *testing.T) {
	t.Parallel()
	s := New("valid_template_name", nil) // nil migrate
	s.boot(t)
	if s.initErr == nil {
		t.Fatal("boot() must populate initErr when migrate func is nil (not panic later)")
	}
	if !strings.Contains(s.initErr.Error(), "migrate func must not be nil") {
		t.Fatalf("initErr %q must mention nil migrate", s.initErr.Error())
	}
}

// ---------------------------------------------------------------------------
// Docker-bound self-tests. Use a pgx-only seed migration so pgclone's own
// tests stay free of any adapters/postgres dependency (the full adapterpg
// migration path is exercised by pgshare's own integration tests).
// ---------------------------------------------------------------------------

const seedTable = "pgclone_seed"

// seedMigrate creates one marker table in the template DB. It MUST close its
// connection before returning per the MigrateFunc contract (CREATE DATABASE
// TEMPLATE rejects a source DB with active connections).
func seedMigrate(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "CREATE TABLE "+seedTable+" (x int)")
	return err
}

var pgcloneSelfTest = New("gocell_pgclone_selftest_template", seedMigrate)

func TestMain(m *testing.M) {
	code := m.Run()
	pgcloneSelfTest.Shutdown()
	os.Exit(code)
}

func tableExists(ctx context.Context, t *testing.T, dsn, table string) bool {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		// DSN omitted from the message — it carries the (test) PG password.
		t.Fatalf("connect to per-test DB %q: %v", table, err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var exists bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		table).Scan(&exists); err != nil {
		t.Fatalf("table-exists probe %s: %v", table, err)
	}
	return exists
}

// TestCloneDSN_IsolatesAndInheritsTemplate boots the shared container (first
// call), clones two per-test DBs, and asserts: (a) both inherit the seed table
// from the migrated template, (b) a write into one is invisible from the other.
func TestCloneDSN_IsolatesAndInheritsTemplate(t *testing.T) {
	ctx := context.Background()
	dsnA := pgcloneSelfTest.CloneDSN(t)
	dsnB := pgcloneSelfTest.CloneDSN(t)

	if !tableExists(ctx, t, dsnA, seedTable) {
		t.Fatalf("clone A did not inherit template seed table %q", seedTable)
	}
	if !tableExists(ctx, t, dsnB, seedTable) {
		t.Fatalf("clone B did not inherit template seed table %q", seedTable)
	}

	connA, err := pgx.Connect(ctx, dsnA)
	if err != nil {
		t.Fatalf("connect A: %v", err)
	}
	defer func() { _ = connA.Close(ctx) }()

	uniq := "iso_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := connA.Exec(ctx, "CREATE TABLE "+uniq+" (x int)"); err != nil {
		t.Fatalf("create table in A: %v", err)
	}
	if tableExists(ctx, t, dsnB, uniq) {
		t.Fatalf("isolation broken: table %s created in clone A is visible from clone B", uniq)
	}
}

// TestEmptyDSN_HasNoTemplateSchema asserts EmptyDSN returns a clean database
// that does NOT inherit the migrated template (template1, not the seed).
func TestEmptyDSN_HasNoTemplateSchema(t *testing.T) {
	ctx := context.Background()
	dsn := pgcloneSelfTest.EmptyDSN(t)
	if tableExists(ctx, t, dsn, seedTable) {
		t.Fatalf("EmptyDSN must be empty; seed table %q must not exist", seedTable)
	}
}

// TestSharedSingleton_ContainerStartsOnce verifies CloneDSN and EmptyDSN share
// the same container — adminDSN is stable across calls and both paths.
func TestSharedSingleton_ContainerStartsOnce(t *testing.T) {
	_ = pgcloneSelfTest.CloneDSN(t)
	dsn1 := pgcloneSelfTest.adminDSN
	if dsn1 == "" {
		t.Fatal("adminDSN empty after first CloneDSN; boot did not populate")
	}
	_ = pgcloneSelfTest.EmptyDSN(t)
	dsn2 := pgcloneSelfTest.adminDSN
	if dsn1 != dsn2 {
		t.Fatalf("adminDSN changed across calls; container re-spawned (dsn1=%q dsn2=%q)", dsn1, dsn2)
	}
}
