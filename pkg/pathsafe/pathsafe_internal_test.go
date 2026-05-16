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

// TestCollectMissingDirs_NormalChain exercises the happy path: all
// intermediate dirs are absent (ENOENT) → the function returns the missing
// chain leaf-first with nil err. The walk also stops when it hits an
// existing ancestor (rolled into this test via the realRoot terminator).
func TestCollectMissingDirs_NormalChain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	leaf := filepath.Join(root, "a", "b", "c")
	missing, err := collectMissingDirs(leaf, root)
	if err != nil {
		t.Fatalf("collectMissingDirs(normal chain): unexpected error: %v", err)
	}
	if len(missing) != 3 {
		t.Fatalf("collectMissingDirs(normal chain): want 3 missing, got %d (%v)", len(missing), missing)
	}
	// Leaf-first order: innermost first → outermost last.
	if missing[0] != leaf {
		t.Errorf("missing[0] = %q, want leaf %q", missing[0], leaf)
	}
}

// TestCollectMissingDirs_StopsAtExisting verifies the early-break behavior:
// once the walk encounters an existing directory, it stops (no further
// ancestor probing) — parents of an existing dir are implicitly present.
func TestCollectMissingDirs_StopsAtExisting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Pre-create root/a so the walk from root/a/b/c stops after recording b, c.
	existing := filepath.Join(root, "a")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(existing, "b", "c")
	missing, err := collectMissingDirs(leaf, root)
	if err != nil {
		t.Fatalf("collectMissingDirs(partial chain): unexpected error: %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("collectMissingDirs(partial chain): want 2 missing, got %d (%v)", len(missing), missing)
	}
}

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
