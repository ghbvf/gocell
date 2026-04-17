package postgres

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errDirFile implements fs.File and fs.ReadDirFile, returning error from ReadDir.
type errDirFile struct{ err error }

func (d errDirFile) Stat() (fs.FileInfo, error)           { return nil, d.err }
func (d errDirFile) Read(_ []byte) (int, error)           { return 0, d.err }
func (d errDirFile) Close() error                         { return nil }
func (d errDirFile) ReadDir(_ int) ([]fs.DirEntry, error) { return nil, d.err }

// readDirErrFS is an fs.FS whose root "." opens as an errDirFile.
type readDirErrFS struct{ err error }

func (r readDirErrFS) Open(name string) (fs.File, error) {
	if name == "." {
		return errDirFile(r), nil
	}
	return nil, r.err
}

// ---------------------------------------------------------------------------
// TestExpectedVersion — unit tests for the FS-scan helper
// ---------------------------------------------------------------------------

func TestExpectedVersion_FromEmbedFS(t *testing.T) {
	// Use the real embedded migrations FS to verify max version detection works.
	// This is a contract test: if a new migration is added, this test
	// automatically uses the new max.
	fsys := MigrationsFS()
	v, err := ExpectedVersion(fsys)
	require.NoError(t, err)
	// Currently 6 migrations (001-006).
	assert.Equal(t, int64(6), v,
		"expected version should be exactly 6 (current migration count)")
}

func TestExpectedVersion_SyntheticFS(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string][]byte
		wantMax int64
	}{
		{
			name: "single migration",
			files: map[string][]byte{
				"001_create_foo.sql": []byte("-- up"),
			},
			wantMax: 1,
		},
		{
			name: "multiple migrations picks max",
			files: map[string][]byte{
				"001_create_foo.sql": []byte("-- up"),
				"003_add_bar.sql":    []byte("-- up"),
				"007_alter_baz.sql":  []byte("-- up"),
			},
			wantMax: 7,
		},
		{
			name:    "empty FS returns 0",
			files:   map[string][]byte{},
			wantMax: 0,
		},
		{
			name: "non-sql files are ignored",
			files: map[string][]byte{
				"README.md":      []byte("docs"),
				"001_create.sql": []byte("-- up"),
			},
			wantMax: 1,
		},
		{
			name: "files without numeric prefix are ignored",
			files: map[string][]byte{
				"create_foo.sql":    []byte("-- up"),
				"002_something.sql": []byte("-- up"),
			},
			wantMax: 2,
		},
		{
			name: "subdirectory entries are skipped",
			files: map[string][]byte{
				"subdir/nested.sql": []byte("-- up"),
				"005_real.sql":      []byte("-- up"),
			},
			wantMax: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := make(fstest.MapFS)
			for name, content := range tc.files {
				fsys[name] = &fstest.MapFile{Data: content}
			}
			got, err := ExpectedVersion(fsys)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMax, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedVersion — unit tests for the validation guard
// ---------------------------------------------------------------------------

// TestVerifyExpectedVersion_InvalidTableName verifies that an invalid SQL
// identifier in the tableName argument is rejected before any DB interaction.
func TestVerifyExpectedVersion_InvalidTableName(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
	}{
		{name: "semicolon injection", tableName: "schema_migrations; DROP TABLE users"},
		{name: "dash in name", tableName: "schema-migrations"},
		{name: "space in name", tableName: "schema migrations"},
		{name: "leading digit", tableName: "1_schema_migrations"},
	}

	fsys := fstest.MapFS{
		"001_create.sql": &fstest.MapFile{Data: []byte("-- +goose Up")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// VerifyExpectedVersion validates tableName before opening any DB connection.
			err := VerifyExpectedVersion(context.Background(), nil, fsys, tc.tableName)
			require.Error(t, err, "invalid tableName should return error")
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "error should be an errcode.Error")
			assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
				"error code should be ErrValidationFailed")
		})
	}
}

// ---------------------------------------------------------------------------
// TestDetectInvalidIndexes — unit tests for the InvalidIndex type
// ---------------------------------------------------------------------------

// TestInvalidIndex_Fields verifies the InvalidIndex struct fields are
// accessible and zero-valued correctly (compile-time + basic sanity).
func TestInvalidIndex_Fields(t *testing.T) {
	idx := InvalidIndex{
		Index: "public.idx_outbox_pending_v2",
		Table: "public.outbox_entries",
	}
	assert.Equal(t, "public.idx_outbox_pending_v2", idx.Index)
	assert.Equal(t, "public.outbox_entries", idx.Table)

	var zero InvalidIndex
	assert.Empty(t, zero.Index)
	assert.Empty(t, zero.Table)
}

// ---------------------------------------------------------------------------
// TestExpectedVersion — error path: ReadDir fails
// ---------------------------------------------------------------------------

// TestExpectedVersion_ReadDirError verifies that ExpectedVersion propagates
// a ReadDir failure from the underlying fs.FS.
func TestExpectedVersion_ReadDirError(t *testing.T) {
	sentinel := errors.New("disk I/O error")
	fsys := readDirErrFS{err: sentinel}

	_, err := ExpectedVersion(fsys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_guard: read migration dir",
		"error must include context prefix")
	assert.ErrorIs(t, err, sentinel, "original error must be wrapped")
}

// TestExpectedVersion_OverflowVersionIgnored verifies that a migration file
// with a numeric prefix too large for int64 is silently skipped (ParseInt
// overflow → parseErr != nil → continue).
func TestExpectedVersion_OverflowVersionIgnored(t *testing.T) {
	// 99999999999999999999 overflows int64.
	fsys := fstest.MapFS{
		"99999999999999999999_too_big.sql": &fstest.MapFile{Data: []byte("-- up")},
		"003_normal.sql":                   &fstest.MapFile{Data: []byte("-- up")},
	}
	v, err := ExpectedVersion(fsys)
	require.NoError(t, err)
	assert.Equal(t, int64(3), v,
		"overflow version must be skipped; normal max must be returned")
}

// ---------------------------------------------------------------------------
// TestVerifyExpectedVersion — error path: ExpectedVersion returns error
// ---------------------------------------------------------------------------

// TestVerifyExpectedVersion_ExpectedVersionError verifies that
// VerifyExpectedVersion propagates a failure from ExpectedVersion
// before ever touching the DB pool.
func TestVerifyExpectedVersion_ExpectedVersionError(t *testing.T) {
	sentinel := errors.New("disk I/O error")
	fsys := readDirErrFS{err: sentinel}

	// Valid table name → passes validateIdentifier, then fails at ExpectedVersion.
	// pool=nil is intentional: we must NOT reach stdlib.OpenDBFromPool.
	err := VerifyExpectedVersion(context.Background(), nil, fsys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema_guard: compute expected version",
		"error must include context prefix")
	assert.ErrorIs(t, err, sentinel, "original error must be wrapped")
}
