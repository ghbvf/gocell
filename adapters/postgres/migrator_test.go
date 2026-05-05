package postgres

import (
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
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

func TestNewMigrator_DefaultTableName(t *testing.T) {
	p := &Pool{inner: nil}
	m, err := NewMigrator(p, stubMigrationFS(), "")
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations", m.tableName)
	_ = m.Close()
}

func TestNewMigrator_CustomTableName(t *testing.T) {
	p := &Pool{inner: nil}
	m, err := NewMigrator(p, stubMigrationFS(), "custom_migrations")
	require.NoError(t, err)
	assert.Equal(t, "custom_migrations", m.tableName)
	_ = m.Close()
}

func TestNewMigrator_InvalidTableName(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
	}{
		{name: "SQL injection attempt", tableName: "schema_migrations; DROP TABLE users--"},
		{name: "starts with digit", tableName: "1invalid"},
		{name: "contains spaces", tableName: "my table"},
		{name: "contains dash", tableName: "my-table"},
		{name: "contains dot", tableName: "schema.table"},
		{name: "contains semicolon", tableName: "table;"},
		{name: "contains parentheses", tableName: "table()"},
		{name: "unicode characters", tableName: "tbl\u00e9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pool{inner: nil}
			m, err := NewMigrator(p, stubMigrationFS(), tt.tableName)
			assert.Nil(t, m)
			require.Error(t, err)

			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

func TestNewMigrator_ValidTableNames(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
	}{
		{name: "lowercase", tableName: "migrations"},
		{name: "with underscore", tableName: "schema_migrations"},
		{name: "starts with underscore", tableName: "_private"},
		{name: "uppercase", tableName: "MIGRATIONS"},
		{name: "mixed case", tableName: "SchemaMigrations"},
		{name: "with digits", tableName: "migrations_v2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pool{inner: nil}
			m, err := NewMigrator(p, stubMigrationFS(), tt.tableName)
			require.NoError(t, err)
			assert.Equal(t, tt.tableName, m.tableName)
			_ = m.Close()
		})
	}
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
	assert.Equal(t, int64(len(versions)), expected,
		"max version (%d) must equal file count (%d) — migrations must be contiguous from 001",
		expected, len(versions))

	for v := int64(1); v <= expected; v++ {
		assert.Contains(t, versions, v, "missing migration with version %03d", v)
	}
}

func TestMigrationDirection_Values(t *testing.T) {
	assert.Equal(t, MigrationDirection("up"), MigrationUp)
	assert.Equal(t, MigrationDirection("down"), MigrationDown)
}
