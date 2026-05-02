// Package struct_field_assign_violates verifies that assigning time.Now to
// a struct field of type `func() time.Time` is flagged. This is the
// runtime/auth/* pattern. 1 violation expected on the line containing the
// composite literal value.
package struct_field_assign_violates

import "time"

type config struct {
	now func() time.Time
}

func newConfig() config {
	return config{now: time.Now}
}
