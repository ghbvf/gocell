package archtest

import (
	"os"
	"path/filepath"
	"testing"
)

// findModuleRoot walks up from the test process's cwd to locate the directory
// containing go.mod. It is the single module-root discovery source shared by
// [RunTyped] (pass.go) and the go-list integration helpers in archtest_test.go.
//
// Failure modes (getwd error, no go.mod above cwd) terminate the test via
// t.Fatalf. archtest drivers are unconditionally fail-loud.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("archtest: findModuleRoot: getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("archtest: findModuleRoot: go.mod not found above %s", dir)
		}
		dir = parent
	}
}
