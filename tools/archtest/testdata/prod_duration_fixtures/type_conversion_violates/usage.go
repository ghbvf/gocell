// Package type_conversion_violates verifies that a literal duration inside a
// type conversion is caught (the inner BinaryExpr still has type time.Duration):
// 1 violation expected.
package type_conversion_violates

import "time"

func f() int64 {
	return int64(5 * time.Second)
}
