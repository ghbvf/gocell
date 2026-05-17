// Package type_conversion_violates verifies that a literal duration inside a
// type conversion is caught (the inner BinaryExpr still has type time.Duration):
// 1 violation expected (declared via spec.Violation()).
package type_conversion_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func f() int64 {
	spec.Violation()
	return int64(5 * time.Second)
}
