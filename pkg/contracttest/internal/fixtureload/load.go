// Package fixtureload encapsulates contract schema/fixture file reads. All
// path inputs are validated by callers in pkg/contracttest against an
// allow-list (in-dir or <contractsRoot>/shared/) before this helper is
// invoked, so the gosec G304 nolint annotation lives at one audit point.
package fixtureload

import "os"

// LoadFixture reads a contract schema or fixture file. The path is
// caller-validated (see pkg/contracttest.compileSchemaFile for the
// allow-list) — both real contracts/ trees and testdata/contracts/ trees
// flow through here.
func LoadFixture(path string) ([]byte, error) {
	return os.ReadFile(path)
}
