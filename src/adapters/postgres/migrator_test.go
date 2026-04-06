package postgres

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMigrationFilename(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		direction MigrationDirection
		wantOK    bool
		wantVer   string
		wantName  string
	}{
		{
			name:      "valid up migration",
			filename:  "001_create_outbox_entries.up.sql",
			direction: MigrationUp,
			wantOK:    true,
			wantVer:   "001",
			wantName:  "create_outbox_entries",
		},
		{
			name:      "valid down migration",
			filename:  "001_create_outbox_entries.down.sql",
			direction: MigrationDown,
			wantOK:    true,
			wantVer:   "001",
			wantName:  "create_outbox_entries",
		},
		{
			name:      "wrong direction suffix",
			filename:  "001_create_outbox_entries.down.sql",
			direction: MigrationUp,
			wantOK:    false,
		},
		{
			name:      "no underscore",
			filename:  "001.up.sql",
			direction: MigrationUp,
			wantOK:    false,
		},
		{
			name:      "empty version",
			filename:  "_foo.up.sql",
			direction: MigrationUp,
			wantOK:    false,
		},
		{
			name:      "not sql file",
			filename:  "001_foo.up.txt",
			direction: MigrationUp,
			wantOK:    false,
		},
		{
			name:      "multi-digit version",
			filename:  "0042_add_index.up.sql",
			direction: MigrationUp,
			wantOK:    true,
			wantVer:   "0042",
			wantName:  "add_index",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf, ok := parseMigrationFilename(tt.filename, tt.direction)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantVer, mf.version)
				assert.Equal(t, tt.wantName, mf.name)
				assert.Equal(t, tt.direction, mf.direction)
				assert.Equal(t, tt.filename, mf.filename)
			}
		})
	}
}

func TestListMigrations_Ordering(t *testing.T) {
	// Create an in-memory FS with migrations in non-sorted order.
	memFS := fstest.MapFS{
		"003_add_column.up.sql":           &fstest.MapFile{Data: []byte("ALTER TABLE ...")},
		"001_create_table.up.sql":         &fstest.MapFile{Data: []byte("CREATE TABLE ...")},
		"002_add_index.up.sql":            &fstest.MapFile{Data: []byte("CREATE INDEX ...")},
		"001_create_table.down.sql":       &fstest.MapFile{Data: []byte("DROP TABLE ...")},
		"002_add_index.down.sql":          &fstest.MapFile{Data: []byte("DROP INDEX ...")},
		"003_add_column.down.sql":         &fstest.MapFile{Data: []byte("ALTER TABLE ...")},
		"README.md":                       &fstest.MapFile{Data: []byte("ignore me")},
	}

	m := &Migrator{migrations: memFS}

	upFiles, err := m.listMigrations(MigrationUp)
	require.NoError(t, err)
	require.Len(t, upFiles, 3)
	assert.Equal(t, "001", upFiles[0].version)
	assert.Equal(t, "002", upFiles[1].version)
	assert.Equal(t, "003", upFiles[2].version)

	downFiles, err := m.listMigrations(MigrationDown)
	require.NoError(t, err)
	require.Len(t, downFiles, 3)
}

func TestListMigrations_EmptyFS(t *testing.T) {
	memFS := fstest.MapFS{}
	m := &Migrator{migrations: memFS}

	files, err := m.listMigrations(MigrationUp)
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestNewMigrator_DefaultTableName(t *testing.T) {
	p := &Pool{inner: nil}
	m, err := NewMigrator(p, fstest.MapFS{}, "")
	require.NoError(t, err)
	assert.Equal(t, "schema_migrations", m.tableName)
}

func TestNewMigrator_CustomTableName(t *testing.T) {
	p := &Pool{inner: nil}
	m, err := NewMigrator(p, fstest.MapFS{}, "custom_migrations")
	require.NoError(t, err)
	assert.Equal(t, "custom_migrations", m.tableName)
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
			m, err := NewMigrator(p, fstest.MapFS{}, tt.tableName)
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
			m, err := NewMigrator(p, fstest.MapFS{}, tt.tableName)
			require.NoError(t, err)
			assert.Equal(t, tt.tableName, m.tableName)
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
	// Verify that MigrationsFS() returns a valid FS with files accessible
	// at the root level (not under migrations/).
	mfs := MigrationsFS()
	require.NotNil(t, mfs)

	m := &Migrator{migrations: mfs}
	files, err := m.listMigrations(MigrationUp)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "001", files[0].version)
	assert.Equal(t, "create_outbox_entries", files[0].name)
	assert.Equal(t, "002", files[1].version)
	assert.Equal(t, "add_topic_column", files[1].name)
}

func TestMigrationDirection_Values(t *testing.T) {
	assert.Equal(t, MigrationDirection("up"), MigrationUp)
	assert.Equal(t, MigrationDirection("down"), MigrationDown)
}

func TestMigrationLockID_IsConstant(t *testing.T) {
	// Verify the advisory lock ID is the expected constant value.
	assert.Equal(t, int64(1234567890), migrationLockID)
}
