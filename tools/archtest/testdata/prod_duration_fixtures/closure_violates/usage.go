// Package closure_violates verifies that a literal duration inside a closure
// body is caught: 1 violation expected (declared via spec.Violation()).
package closure_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

var doWork = func() {
	spec.Violation()
	time.Sleep(5 * time.Second)
}
