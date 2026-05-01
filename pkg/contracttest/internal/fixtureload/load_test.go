package fixtureload

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFixture(t *testing.T) {
	tmp := t.TempDir()

	t.Run("reads existing file", func(t *testing.T) {
		path := filepath.Join(tmp, "ok.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"hello":"world"}`), 0o644))

		data, err := LoadFixture(path)
		require.NoError(t, err)
		assert.Equal(t, `{"hello":"world"}`, string(data))
	})

	t.Run("returns empty bytes for empty file", func(t *testing.T) {
		path := filepath.Join(tmp, "empty.json")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		data, err := LoadFixture(path)
		require.NoError(t, err)
		assert.Empty(t, data)
	})

	t.Run("fails closed for missing file", func(t *testing.T) {
		_, err := LoadFixture(filepath.Join(tmp, "does-not-exist.json"))
		require.Error(t, err)
		assert.True(t, os.IsNotExist(err), "expected os.IsNotExist, got %v", err)
	})

	t.Run("propagates permission denied", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows file permissions don't enforce 0o000 read denial")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root; permission bits are bypassed")
		}
		path := filepath.Join(tmp, "denied.json")
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o000))
		restorePerm := os.FileMode(0o644)
		t.Cleanup(func() { _ = os.Chmod(path, restorePerm) })

		_, err := LoadFixture(path)
		require.Error(t, err)
		assert.True(t, os.IsPermission(err), "expected os.IsPermission, got %v", err)
	})
}
