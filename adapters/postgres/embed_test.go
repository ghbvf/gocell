package postgres

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

func testMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := MigrationsFS()
	require.NoError(t, err)
	return fsys
}
