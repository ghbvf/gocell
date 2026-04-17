package postgres

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
