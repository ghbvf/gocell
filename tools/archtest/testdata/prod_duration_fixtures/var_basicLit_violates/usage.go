// Package var_basicLit_violates verifies that a bare numeric BasicLit assigned
// to a time.Duration variable is caught (single-node BasicLit case):
// 1 violation expected (declared via spec.Violation()).
package var_basicLit_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() { spec.Violation() }

var defaultWait time.Duration = 5
