// Package fixtureload encapsulates contract test fixture loading. All path
// inputs are validated by callers in pkg/contracttest as testdata/-rooted
// (controlled input).
package fixtureload

import "os"

// LoadFixture reads a contract test fixture from a controlled testdata/ path.
func LoadFixture(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // G304: contract test fixture path validated by caller (testdata/-rooted)
}
