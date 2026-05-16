package pathsafe

// Internal (package pathsafe) tests for Lane A unexported helpers:
//   - collectMissingDirs (A9): EACCES on intermediate parent must propagate
//     as a non-nil error, not be swallowed as "directory exists / break".
//
// Skip windows + root: chmod 0o000 not reliable on windows; ineffective as
// root. Sequential (not Parallel) because chmod is process-global state.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// EACCES on an intermediate dir must surface as an error, not be silently
// dropped via the os.IsNotExist(err) check (the develop @ 41fc70074 bug).
func TestCollectMissingDirs_EACCESReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 semantics differ on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("chmod 0o000 ineffective as root")
	}

	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("Chmod 0o000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	// Target dir lies under the 0o000 parent — os.Stat on it returns EACCES
	// (parent has no execute bit), NOT ENOENT.
	target := filepath.Join(blocked, "sub", "leaf")
	missing, err := collectMissingDirs(target, root)
	if err == nil {
		t.Fatalf("collectMissingDirs(target under 0o000 parent): want error, got nil (missing=%v)", missing)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("EACCES misclassified as not-exist: err=%v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("expected fs.ErrPermission, got err=%v", err)
	}
}
