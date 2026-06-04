package postgres

import (
	"context"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/migration"
)

// stubMigrationFS returns a minimal valid goose-annotated migration FS
// suitable for unit tests that only validate constructor behavior.
func stubMigrationFS() fstest.MapFS {
	return fstest.MapFS{
		"001_stub.sql": &fstest.MapFile{
			Data: []byte("-- +goose Up\n-- noop\n-- +goose Down\n-- noop\n"),
		},
	}
}

func TestNewMigrator_DerivesTrackingTableFromNamespace(t *testing.T) {
	p := &Pool{inner: nil}

	m, err := NewMigrator(p, stubMigrationFS(), migration.PlatformNamespace)
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations_platform", m.tableName,
		"platform namespace must derive schema_migrations_platform (no special-casing)")
	_ = m.Close()

	ns, err := migration.ParseNamespace("payment")
	require.NoError(t, err)
	m2, err := NewMigrator(p, stubMigrationFS(), ns)
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations_payment", m2.tableName,
		"external namespace must derive schema_migrations_<namespace>")
	_ = m2.Close()
}

func TestNewMigrator_InvalidNamespace(t *testing.T) {
	// A typed migration.Namespace makes omission / bare-string a COMPILE error;
	// these invalid VALUES (incl. the Namespace("x") conversion-literal escape)
	// are caught fail-fast at runtime by NewMigrator's ns.Validate().
	tests := []struct {
		name string
		ns   migration.Namespace
	}{
		{name: "empty (zero value)", ns: migration.Namespace("")},
		{name: "dash conversion-literal escape", ns: migration.Namespace("acme-payment")},
		{name: "uppercase", ns: migration.Namespace("Payment")},
		{name: "leading digit", ns: migration.Namespace("2pay")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pool{inner: nil}
			m, err := NewMigrator(p, stubMigrationFS(), tt.ns)
			assert.Nil(t, m)
			require.Error(t, err)

			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, ErrAdapterPGMigrate, ecErr.Code)
		})
	}
}

func TestNewMigratorForTable_InvalidTableName(t *testing.T) {
	// newMigratorForTable is the unexported in-package escape hatch for tests
	// that need isolated tracking tables; it still validates the table identifier
	// (production code reaches it only via NewMigrator's trackingTableFor(ns)).
	tests := []struct {
		name      string
		tableName string
	}{
		{name: "SQL injection attempt", tableName: "schema_migrations; DROP TABLE users--"},
		{name: "starts with digit", tableName: "1invalid"},
		{name: "contains spaces", tableName: "my table"},
		{name: "contains dash", tableName: "my-table"},
		{name: "contains dot", tableName: "schema.table"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pool{inner: nil}
			m, err := newMigratorForTable(p, stubMigrationFS(), tt.tableName)
			assert.Nil(t, m)
			require.Error(t, err)

			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

func TestNewMigratorForTable_ValidTableName(t *testing.T) {
	p := &Pool{inner: nil}
	m, err := newMigratorForTable(p, stubMigrationFS(), "schema_migrations_iso_probe")
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations_iso_probe", m.tableName)
	_ = m.Close()
}

func TestValidateIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "simple", input: "users", wantErr: false},
		{name: "underscore prefix", input: "_temp", wantErr: false},
		{name: "with digits", input: "table2", wantErr: false},
		{name: "all caps", input: "SCHEMA_MIGRATIONS", wantErr: false},
		{name: "empty string", input: "", wantErr: true},
		{name: "starts with digit", input: "1foo", wantErr: true},
		{name: "contains space", input: "foo bar", wantErr: true},
		{name: "SQL injection", input: "t; DROP TABLE x", wantErr: true},
		{name: "dot notation", input: "public.users", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIdentifier(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMigrationsFS_SubDirectory(t *testing.T) {
	// Verify that testMigrationsFS(t) returns a valid FS with goose-annotated
	// migrations contiguously numbered from 001 upward. Hard-coding the count
	// here would force a mechanical CI red on every additive migration PR
	// without protecting any architectural invariant; instead we assert the
	// file set is dense (no gaps) and matches the FS-derived ExpectedVersion.
	mfs := testMigrationsFS(t)
	require.NotNil(t, mfs)

	entries, err := fs.ReadDir(mfs, ".")
	require.NoError(t, err)

	versions := make(map[int64]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationVersionRe.FindStringSubmatch(name)
		require.NotNil(t, m, "%s must match the NNN_*.sql migration naming convention", name)
		v, parseErr := strconv.ParseInt(m[1], 10, 64)
		require.NoError(t, parseErr, "version prefix in %s must parse as int64", name)
		require.NotContains(t, versions, v, "duplicate migration version %d (%s vs %s)", v, versions[v], name)
		versions[v] = name
	}

	require.NotEmpty(t, versions, "migrations FS must contain at least one .sql file")

	expected, err := ExpectedVersion(mfs)
	require.NoError(t, err)

	// knownGaps records migration version numbers that are intentionally absent.
	// Add an entry here only when an in-flight PR has reserved the slot but
	// the migration file has not yet landed; remove the entry once the gap
	// closes (e.g. PR #464 reserved 022 → S6 merged → entry removed).
	// Empty map = contiguous migrations, the steady state.
	knownGaps := map[int64]string{
		// 033 landed on develop in PR #1007 (password_version >= 0 CHECK);
		// 034-039 remain reserved for parallel PRs preceding saga 040.
		34: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
		35: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
		36: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
		37: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
		38: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
		39: "saga/L3 plan §R2 — reserved for parallel PRs preceding 040 (PR-04 #959)",
	}

	// Max version must equal file count plus known-gap count.
	assert.Equal(t, int64(len(versions)+len(knownGaps)), expected,
		"max version (%d) must equal file count (%d) + known gaps (%d)",
		expected, len(versions), len(knownGaps))

	for v := int64(1); v <= expected; v++ {
		if _, ok := knownGaps[v]; ok {
			continue // intentionally absent
		}
		assert.Contains(t, versions, v, "missing migration with version %03d", v)
	}
}

func TestMigrationDirection_Values(t *testing.T) {
	assert.Equal(t, MigrationDirection("up"), MigrationUp)
	assert.Equal(t, MigrationDirection("down"), MigrationDown)
}

func TestAllowDestructiveDown(t *testing.T) {
	permit, err := AllowDestructiveDown("  approved rollback  ")
	require.NoError(t, err)
	assert.Equal(t, "approved rollback", permit.Reason())

	permit, err = AllowDestructiveDown(" \t ")
	require.Error(t, err)
	assert.Nil(t, permit)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestMigrator_Down_RequiresDestructiveDownPermit(t *testing.T) {
	err := (&Migrator{}).Down(context.Background(), nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestAllowForwardRebuild(t *testing.T) {
	permit, err := AllowForwardRebuild(43, "  audit_entries v2 populated rebuild  ")
	require.NoError(t, err)
	assert.Equal(t, int64(43), permit.MigrationNumber())
	assert.Equal(t, "audit_entries v2 populated rebuild", permit.Reason(),
		"reason must be trimmed, matching AllowDestructiveDown")

	// Empty reason is rejected — every break-glass permit must carry an audit trail.
	permit, err = AllowForwardRebuild(43, " \t ")
	require.Error(t, err)
	assert.Nil(t, permit)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)

	// Non-positive migration number is rejected — goose Source.Version starts at 1.
	permit, err = AllowForwardRebuild(0, "valid reason")
	require.Error(t, err)
	assert.Nil(t, permit)
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// ---------------------------------------------------------------------------
// [F17·Cx1] TestAllowForwardRebuild_NegativeNumber
// ---------------------------------------------------------------------------

// TestAllowForwardRebuild_NegativeNumber supplements TestAllowForwardRebuild
// with a negative migration number edge case — AllowForwardRebuild checks
// migrationNumber <= 0, so both 0 and negative values must be rejected.
func TestAllowForwardRebuild_NegativeNumber(t *testing.T) {
	permit, err := AllowForwardRebuild(-5, "valid reason")
	require.Error(t, err, "negative migration number must be rejected")
	assert.Nil(t, permit)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
		"must surface ErrValidationFailed for negative migration number")
}

// ---------------------------------------------------------------------------
// [F7·Cx2] TestBuildPermitMap
// ---------------------------------------------------------------------------

// TestBuildPermitMap tests the unexported buildPermitMap directly.
// forwardRebuildPermit is a value type (not a pointer), so a typed-nil element
// cannot be constructed from outside the package; the nil element test uses an
// untyped nil ([]ForwardRebuildPermit{nil}), which exercises the p == nil
// branch in buildPermitMap.
func TestBuildPermitMap(t *testing.T) {
	makePermit := func(t *testing.T, num int64) ForwardRebuildPermit {
		t.Helper()
		p, err := AllowForwardRebuild(num, "test permit")
		require.NoError(t, err)
		return p
	}

	tests := []struct {
		name        string
		permits     []ForwardRebuildPermit
		wantErr     bool
		wantErrCode errcode.Code
		wantMsgHint string // substring expected in error message or code description
		wantLen     int
		wantKeys    []int64
	}{
		{
			name:        "nil element rejected",
			permits:     []ForwardRebuildPermit{nil},
			wantErr:     true,
			wantErrCode: ErrAdapterPGMigrate,
		},
		{
			name: "duplicate migration number rejected",
			permits: func() []ForwardRebuildPermit {
				p1 := makePermit(t, 43)
				p2 := makePermit(t, 43)
				return []ForwardRebuildPermit{p1, p2}
			}(),
			wantErr:     true,
			wantErrCode: ErrAdapterPGMigrate,
		},
		{
			name: "two different permits succeed",
			permits: func() []ForwardRebuildPermit {
				return []ForwardRebuildPermit{makePermit(t, 43), makePermit(t, 44)}
			}(),
			wantErr:  false,
			wantLen:  2,
			wantKeys: []int64{43, 44},
		},
		{
			name:    "empty slice returns empty map",
			permits: []ForwardRebuildPermit{},
			wantErr: false,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := buildPermitMap(tt.permits)
			if tt.wantErr {
				require.Error(t, err)
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec)
				assert.Equal(t, tt.wantErrCode, ec.Code)
				return
			}
			require.NoError(t, err)
			assert.Len(t, m, tt.wantLen)
			for _, k := range tt.wantKeys {
				_, ok := m[k]
				assert.Truef(t, ok, "expected key %d in permit map", k)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// [F8·Cx2·关键] TestForwardRebuildTargets_UpSectionOnly
// ---------------------------------------------------------------------------

// TestForwardRebuildTargets_UpSectionOnly verifies that forwardRebuildTargets
// only scans the Up section and is not tricked by:
//   - an annotation in the Down section (must not match)
//   - a literal "-- +goose Down" substring appearing inside Up-section prose
//     (must not truncate the scan — fail-open guard fixed by line-anchored RE).
//   - multiple annotation lines (multi-target rebuild) — all must be returned.
func TestForwardRebuildTargets_UpSectionOnly(t *testing.T) {
	tests := []struct {
		name        string
		sqlContent  string
		wantTargets []string
		wantOK      bool
	}{
		{
			name: "single annotation in Up section returns one target",
			sqlContent: "-- +goose Up\n" +
				"-- +gocell forward-rebuild target=my_table\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"SELECT 2;\n",
			wantTargets: []string{"my_table"},
			wantOK:      true,
		},
		{
			name: "multiple annotations in Up section returns all targets",
			sqlContent: "-- +goose Up\n" +
				"-- +gocell forward-rebuild target=table_a\n" +
				"-- +gocell forward-rebuild target=table_b\n" +
				"-- +gocell forward-rebuild target=table_c\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"SELECT 2;\n",
			wantTargets: []string{"table_a", "table_b", "table_c"},
			wantOK:      true,
		},
		{
			name: "annotation only in Down section is ignored",
			sqlContent: "-- +goose Up\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"-- +gocell forward-rebuild target=down_table\n" +
				"SELECT 2;\n",
			wantTargets: nil,
			wantOK:      false,
		},
		{
			// Fail-open regression: two non-marker "+goose Down" prose forms (line-prefix and word-boundary) in Up-section
			// prose must NOT truncate the scan. Only a line-anchored "^-- +goose Down$"
			// acts as the section boundary. The annotation appears after the prose
			// comment and must still be detected.
			name: "line-prefixed +goose Down in Up-section prose does not truncate scan",
			sqlContent: "-- +goose Up\n" +
				"-- +goose Down is mentioned here in prose, not as a real section marker\n" +
				"-- +goose Downstream is a word in prose, not the section marker\n" +
				"-- +gocell forward-rebuild target=real_target\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"SELECT 2;\n",
			wantTargets: []string{"real_target"},
			wantOK:      true,
		},
		{
			name: "no annotation returns nil and false",
			sqlContent: "-- +goose Up\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"SELECT 2;\n",
			wantTargets: nil,
			wantOK:      false,
		},
		{
			// Multi-target migration: annotations before Down marker, Down-section annotation ignored.
			name: "multi-target Up with Down-section annotation — Down annotation excluded",
			sqlContent: "-- +goose Up\n" +
				"-- +gocell forward-rebuild target=alpha\n" +
				"-- +gocell forward-rebuild target=beta\n" +
				"SELECT 1;\n" +
				"-- +goose Down\n" +
				"-- +gocell forward-rebuild target=should_be_ignored\n" +
				"SELECT 2;\n",
			wantTargets: []string{"alpha", "beta"},
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const fileName = "001_test_migration.sql"
			mfs := fstest.MapFS{
				fileName: &fstest.MapFile{Data: []byte(tt.sqlContent)},
			}
			m := &Migrator{migrations: mfs}
			src := &goose.Source{Path: fileName, Version: 1}

			targets, ok, err := m.forwardRebuildTargets(src)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantTargets, targets)
		})
	}
}
