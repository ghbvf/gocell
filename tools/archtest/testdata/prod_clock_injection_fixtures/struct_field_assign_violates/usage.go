// Package struct_field_assign_violates verifies that assigning time.Now to
// a struct field of type `func() time.Time` is flagged. This is the
// runtime/auth/* pattern.
// 1 violation expected (declared via spec.Violation()).
package struct_field_assign_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

type config struct {
	now func() time.Time
}

func newConfig() config {
	spec.Violation()
	return config{now: time.Now}
}
