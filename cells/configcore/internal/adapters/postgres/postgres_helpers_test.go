//go:build integration

package postgres

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
)

func testAdapterMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}
