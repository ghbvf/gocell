// Package dot_import_violates verifies that a dot-import of time does not
// bypass the gate (the call function is a bare *ast.Ident with type-resolved
// pkg path "time"): 1 violation expected (declared via spec.Violation()).
package dot_import_violates

import (
	. "time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func now() Time {
	spec.Violation()
	return Now()
}
