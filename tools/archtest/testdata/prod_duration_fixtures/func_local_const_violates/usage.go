// Package func_local_const_violates verifies that a function-local const
// initializer is NOT a compliant position:
// 1 violation expected (declared via spec.Violation()).
package func_local_const_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func f() {
	spec.Violation()
	const localTimeout = 5 * time.Second
	time.Sleep(localTimeout)
}
