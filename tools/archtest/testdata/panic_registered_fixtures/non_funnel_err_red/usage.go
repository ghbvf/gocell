// Package non_funnel_err_red verifies that panic(err) without Approved wrap
// is caught: 1 violation expected (declared via spec.Violation()).
package non_funnel_err_red

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func foo(err error) {
	spec.Violation()
	panic(err)
}
