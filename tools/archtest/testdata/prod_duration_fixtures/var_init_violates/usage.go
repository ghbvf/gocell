// Package var_init_violates verifies that a package-level var initializer
// with a literal duration is caught:
// 1 violation expected (declared via spec.Violation()).
package var_init_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() { spec.Violation() }

var defaultWait = 5 * time.Second
