package postgres

import (
	"testing"
	"testing/fstest"

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
	// Currently 5 migrations (001-005); 006 will be added in T4.
	assert.GreaterOrEqual(t, v, int64(5),
		"expected version should be at least 5 (current migration count)")
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
