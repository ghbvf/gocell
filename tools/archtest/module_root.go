package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// [RunTyped] (pass.go) and the go-list integration helpers in archtest_test.go.
//
// Failure modes (getwd error, no go.mod above cwd) terminate the test via
// t.Fatalf. archtest drivers are unconditionally fail-loud.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := lookupModuleRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return root
}

// moduleImportPath parses the "module" directive from root/go.mod and returns
// the declared import path (e.g. "github.com/ghbvf/gocell"). It is the
// production-code module-path source for [RunTypedProduction], which must hand
// the path to typeseval.LoadProductionPackages so the <module>/generated/
// prefix can be computed. Hardcoding the path would silently mis-filter on a
// module rename or /v2 bump, so it is always read from go.mod.
func moduleImportPath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(filepath.Join(root, "go.mod")))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("go.mod at %s has no module directive", root)
}
