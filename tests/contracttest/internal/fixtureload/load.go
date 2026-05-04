// Package fixtureload encapsulates contract schema/fixture file reads. All
// path inputs are validated by callers in tests/contracttest against an
// allow-list (in-dir or <contractsRoot>/shared/) before this helper is
// invoked.
package fixtureload

import (
	"os"
	"path/filepath"
)

// LoadFixture reads a contract schema or fixture file. The path is
// caller-validated (see tests/contracttest.compileSchemaFile for the
// allow-list) — both real contracts/ trees and testdata/contracts/ trees
// flow through here.
func LoadFixture(path string) ([]byte, error) {
	return os.ReadFile(filepath.Clean(path))
}
