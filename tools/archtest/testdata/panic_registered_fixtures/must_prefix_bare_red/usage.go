// Package must_prefix_bare_red verifies that Must*-prefixed functions no
// longer receive an exemption — a bare panic inside MustFoo is caught:
// 1 violation expected (declared via spec.Violation()).
package must_prefix_bare_red

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func MustFoo() {
	spec.Violation()
	panic("bare")
}
