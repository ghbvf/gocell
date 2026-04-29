// Package time_now_add_literal_violates verifies that time.Now().Add(literal)
// is caught (the literal inside Add is a time.Duration expression): 1 violation.
package time_now_add_literal_violates

import "time"

func cutoff() time.Time {
	return time.Now().Add(5 * time.Second)
}
