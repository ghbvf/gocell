package scaffoldfs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionConstants(t *testing.T) {
	cases := []struct {
		name string
		got  os.FileMode
		want os.FileMode
	}{
		{"FileMode is 0o644", FileMode, 0o644},
		{"DirMode is 0o755", DirMode, 0o755},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.got, "permission contract drifted; downstream scaffold/generate would write files with the wrong mode")
		})
	}
}

func TestFileModeAppliedToWrittenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes are not POSIX permission bits")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "scaffold-source.go")
	require.NoError(t, os.WriteFile(path, []byte("package x\n"), FileMode))

	info, err := os.Stat(path)
	require.NoError(t, err)
	// umask may strip group/world write bits at write time; the contract is
	// that scaffolded source code is at minimum owner-readable+writable and
	// world-readable so multi-user CI runners and git can read it.
	mode := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0o600), mode&0o600, "owner read+write must be set")
	assert.Equal(t, os.FileMode(0o004), mode&0o004, "world-readable bit must be set (git + CI)")
}

func TestDirModeAppliedToCreatedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes are not POSIX permission bits")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "scaffold-pkg")
	require.NoError(t, os.MkdirAll(dir, DirMode))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	mode := info.Mode().Perm()
	// Executable bit is required on directories so callers can traverse;
	// world-execute lets git + multi-user CI runners enter the tree.
	assert.Equal(t, os.FileMode(0o700), mode&0o700, "owner rwx must be set")
	assert.Equal(t, os.FileMode(0o005), mode&0o005, "world rx must be set (traversal)")
}
