//go:build integration

package configwrite

import (
	"io/fs"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/stretchr/testify/require"
)

func testAdapterMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}
