// Package dot_import_violates verifies that dot-import of time does not
// bypass the gate: 1 violation expected (declared via spec.Violation()).
package dot_import_violates

import (
	. "time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func f() {
	spec.Violation()
	Sleep(5 * Second)
}
