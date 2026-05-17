// Package bare_string_red verifies that a bare panic("literal") is caught:
// 1 violation expected (declared via spec.Violation()).
package bare_string_red

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func foo() {
	spec.Violation()
	panic("bare")
}
