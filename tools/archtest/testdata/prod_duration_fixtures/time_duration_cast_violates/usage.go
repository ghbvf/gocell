// Package time_duration_cast_violates verifies that time.Duration(30)*time.Second
// is caught: the inner cast time.Duration(30) has type time.Duration and
// isLiteralDurationExpr returns true for it:
// 1 violation expected (declared via spec.Violation()).
package time_duration_cast_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func init() { spec.Violation() }

var defaultWait = time.Duration(30) * time.Second
