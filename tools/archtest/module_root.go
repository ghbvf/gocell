package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/tools/gomodutil"
)

// lookupModuleRoot walks up from the process's cwd to locate the directory
// containing go.mod. On failure it returns an error rather than calling
// t.Fatalf, making it safe to call from non-test contexts such as TestMain.
func lookupModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("archtest: lookupModuleRoot: getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("archtest: lookupModuleRoot: go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// findModuleRoot is a thin wrapper around [lookupModuleRoot] that converts
// errors to t.Fatalf. It is the single module-root discovery source shared by
// [Run] (pass.go) and the go-list integration helpers in archtest_test.go.
//
// The parameter is [testing.TB] (not [*testing.T]) so the unified [Run] driver
// — which accepts testing.TB to support the StandaloneModule fatal-path spy
// tests — can resolve the module root for its Typed / Production / Fixture
// scopes through this single source.
//
// Failure modes (getwd error, no go.mod above cwd) terminate the test via
// t.Fatalf. archtest drivers are unconditionally fail-loud.
func findModuleRoot(t testing.TB) string {
	t.Helper()
	root, err := lookupModuleRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return root
}

// moduleImportPath returns the declared import path (e.g. "github.com/ghbvf/gocell")
// from root/go.mod. It is the production-code module-path source for the
// [Production] scope of [Run], which must hand the path to
// typeseval.LoadProductionPackages so the <module>/generated/ prefix can be
// computed. Hardcoding the path would silently mis-filter on a module rename or
// /v2 bump, so it is always read from go.mod — via gomodutil.ReadModulePath, the
// single shared parser used by codegen, scaffold, the CLI, and archtest.
func moduleImportPath(root string) (string, error) {
	return gomodutil.ReadModulePath(root)
}
