// Package alias_import_violates verifies that aliased time import does not
// bypass the gate: 1 violation expected (declared via spec.Violation()).
package alias_import_violates

import (
	t "time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func f() {
	spec.Violation()
	t.Sleep(5 * t.Second)
}
