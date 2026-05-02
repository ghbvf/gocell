// Package after_violates verifies that time.After is flagged: 1 violation expected.
package after_violates

import "time"

func block(d time.Duration) {
	<-time.After(d)
}
