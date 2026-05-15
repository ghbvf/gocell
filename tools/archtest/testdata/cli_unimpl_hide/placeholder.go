// Fixture for CLI-UNIMPL-HIDE-01 reverse self-check (no-placeholder).
//
// A reachable "not implemented" string return — the removed B2-X-05
// shape. scanNoNotImplementedLiteral MUST flag the literal. Not compiled
// (testdata).
package fixture

import "fmt"

func generateIndexes() error {
	return fmt.Errorf("not implemented: gocell generate indexes")
}
