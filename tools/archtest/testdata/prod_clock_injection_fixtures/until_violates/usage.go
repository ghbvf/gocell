// Package until_violates is a fixture for archtest negative case:
// verifies that time.Until is flagged: 1 violation expected.
package until_violates

import "time"

func remaining(t time.Time) time.Duration {
	return time.Until(t)
}
